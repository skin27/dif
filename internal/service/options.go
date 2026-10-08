// Package service hosts a fixed group of flows as a foreground service.
package service

import (
	"bytes"
	"dif/api"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

const Usage = `Usage: dif run [options]
  --file PATH              DIL JSON file (repeatable)
  --dir PATH               Directory of visible *.json flows (repeatable)
  --url URL                Raw HTTPS DIL document (repeatable)
  --config PATH            Strict JSON service configuration
  --sha256 HEX             Expected digest (requires exactly one URL)
  --monitor-address ADDR   Read-only monitoring listener; disabled by default
  --startup-timeout 30s    Total input loading and startup budget
  --shutdown-timeout 25s   Total drain and shutdown budget
  --fetch-timeout 10s      Per-document HTTP timeout
  --max-bytes 4194304      Maximum bytes per flow document
  --log-format json        json or text
  --output text           Validation output: text or json

dif validate accepts these input options and positional filenames.
Precedence: flags > DIF_* environment > config > defaults.
Paths are relative to the process working directory. run requires explicit input flags.`

type Options struct {
	Channels        *api.ChannelConfig `json:"channels,omitempty"`
	Files           []string           `json:"files"`
	Dirs            []string           `json:"dirs"`
	URLs            []string           `json:"urls"`
	SHA256          string             `json:"sha256"`
	MonitorAddress  string             `json:"monitorAddress"`
	StartupTimeout  string             `json:"startupTimeout"`
	ShutdownTimeout string             `json:"shutdownTimeout"`
	FetchTimeout    string             `json:"fetchTimeout"`
	MaxBytes        int64              `json:"maxBytes"`
	LogFormat       string             `json:"logFormat"`
	Output          string             `json:"-"`
}

type listFlag struct {
	values *[]string
	set    bool
}

func (f *listFlag) String() string { return strings.Join(*f.values, ",") }
func (f *listFlag) Set(s string) error {
	if !f.set {
		*f.values = nil
		f.set = true
	}
	*f.values = append(*f.values, s)
	return nil
}

func decode(data []byte, dst any) error {
	if trimmed := bytes.TrimSpace(data); len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("configuration must be a JSON object")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON document")
	}
	return nil
}

func ParseOptions(args []string, getenv func(string) string, validation bool) (Options, error) {
	o := Options{StartupTimeout: "30s", ShutdownTimeout: "25s", FetchTimeout: "10s", MaxBytes: 4 << 20, LogFormat: "json", Output: "text"}
	config := getenv("DIF_CONFIG")
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--help" || a == "-h" {
			return o, flag.ErrHelp
		}
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "-") {
			name, value, equals := strings.Cut(strings.TrimLeft(a, "-"), "=")
			if !equals {
				i++
				if i == len(args) {
					return o, fmt.Errorf("--%s needs a value", name)
				}
				value = args[i]
			}
			if name == "config" {
				config = value
			}
		}
	}
	if config != "" {
		f, err := os.Open(config)
		if err != nil {
			return o, fmt.Errorf("open service configuration: %w", err)
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			f.Close()
			return o, errors.New("service configuration must be a regular file")
		}
		data, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
		f.Close()
		if err != nil || len(data) > 4<<20 {
			return o, errors.New("cannot read service configuration within 4 MiB limit")
		}
		if err := decode(data, &o); err != nil {
			return o, fmt.Errorf("service configuration: %w", err)
		}
	}
	for name, dst := range map[string]*[]string{"DIF_FILES": &o.Files, "DIF_DIRS": &o.Dirs, "DIF_URLS": &o.URLs} {
		if value := getenv(name); value != "" {
			if err := json.Unmarshal([]byte(value), dst); err != nil {
				return o, fmt.Errorf("%s must be a JSON string array", name)
			}
		}
	}
	for name, dst := range map[string]*string{"DIF_SHA256": &o.SHA256, "DIF_MONITOR_ADDRESS": &o.MonitorAddress, "DIF_STARTUP_TIMEOUT": &o.StartupTimeout, "DIF_SHUTDOWN_TIMEOUT": &o.ShutdownTimeout, "DIF_FETCH_TIMEOUT": &o.FetchTimeout, "DIF_LOG_FORMAT": &o.LogFormat} {
		if value := getenv(name); value != "" {
			*dst = value
		}
	}
	if value := getenv("DIF_MAX_BYTES"); value != "" {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return o, errors.New("DIF_MAX_BYTES must be an integer")
		}
		o.MaxBytes = n
	}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Var(&listFlag{values: &o.Files}, "file", "file")
	fs.Var(&listFlag{values: &o.Dirs}, "dir", "directory")
	fs.Var(&listFlag{values: &o.URLs}, "url", "URL")
	fs.StringVar(&config, "config", config, "config")
	fs.StringVar(&o.SHA256, "sha256", o.SHA256, "digest")
	fs.StringVar(&o.MonitorAddress, "monitor-address", o.MonitorAddress, "address")
	fs.StringVar(&o.StartupTimeout, "startup-timeout", o.StartupTimeout, "duration")
	fs.StringVar(&o.ShutdownTimeout, "shutdown-timeout", o.ShutdownTimeout, "duration")
	fs.StringVar(&o.FetchTimeout, "fetch-timeout", o.FetchTimeout, "duration")
	fs.Int64Var(&o.MaxBytes, "max-bytes", o.MaxBytes, "bytes")
	fs.StringVar(&o.LogFormat, "log-format", o.LogFormat, "format")
	if validation {
		fs.StringVar(&o.Output, "output", o.Output, "format")
	}
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") {
			name := strings.TrimLeft(strings.SplitN(a, "=", 2)[0], "-")
			if fs.Lookup(name) == nil {
				return o, fmt.Errorf("unknown option --%s", name)
			}
			flags = append(flags, a)
			if !strings.Contains(a, "=") {
				i++
				if i == len(args) {
					return o, fmt.Errorf("--%s needs a value", name)
				}
				flags = append(flags, args[i])
			}
		} else {
			positional = append(positional, a)
		}
	}
	if err := fs.Parse(flags); err != nil {
		return o, errors.New("invalid service option value")
	}
	if len(positional) > 0 && !validation {
		return o, errors.New("run accepts --file, --dir or --url; unexpected positional argument")
	}
	o.Files = append(o.Files, positional...)
	return o, o.Validate()
}

func (o Options) Validate() error {
	if o.Channels != nil {
		if err := api.ValidateChannelConfig(*o.Channels); err != nil {
			return fmt.Errorf("channels: %w", err)
		}
	}
	if len(o.Files)+len(o.Dirs)+len(o.URLs) == 0 {
		return errors.New("provide at least one --file, --dir or --url")
	}
	for _, values := range [][]string{o.Files, o.Dirs, o.URLs} {
		for _, v := range values {
			if strings.TrimSpace(v) == "" {
				return errors.New("input must not be empty")
			}
		}
	}
	for _, v := range []string{o.StartupTimeout, o.ShutdownTimeout, o.FetchTimeout} {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return errors.New("timeouts must be positive Go durations, such as 30s")
		}
	}
	if o.MaxBytes <= 0 || o.MaxBytes > 64<<20 {
		return errors.New("max-bytes must be between 1 and 67108864")
	}
	if o.LogFormat != "json" && o.LogFormat != "text" {
		return errors.New("log-format must be json or text")
	}
	if o.Output != "" && o.Output != "text" && o.Output != "json" {
		return errors.New("output must be text or json")
	}
	if o.SHA256 != "" && len(o.URLs) != 1 {
		return errors.New("sha256 requires exactly one URL")
	}
	if o.SHA256 != "" {
		digest, err := hex.DecodeString(o.SHA256)
		if err != nil || len(digest) != 32 {
			return errors.New("sha256 must contain 64 hexadecimal characters")
		}
	}
	return nil
}

func duration(s string) time.Duration { d, _ := time.ParseDuration(s); return d }
