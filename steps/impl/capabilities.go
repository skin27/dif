package impl

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	stepdef "dif/steps/definition"
)

// stepCapabilities says which outside resources the built-in steps can touch,
// by name/kind as they are registered: the addresses they connect to or listen
// on, the files they read or write and the environment variables they read.
// Each entry declares what the step's code can do, not what it usually does;
// a step that touches nothing outside the process declares none. Every
// built-in step must be here: Register refuses one that is not.
//
// Channels (queue, topic, flowlink, service, ...), stores, tenant variables
// and the flow's log are inside the process: their storage, the journal of a
// durable queue or store too, is the service's configuration, not the
// program's.
var stepCapabilities = map[string]func(stepdef.Params) []stepdef.Capability{
	// Sources.
	"timer/source":       none,
	"repeater/source":    none,
	"counter/source":     none,
	"quartz/source":      none,
	"message/source":     none,
	"queue/source":       none,
	"reply/source":       none,
	"topic/source":       none,
	"event/source":       none,
	"flowlink/source":    none,
	"service/source":     none,
	"file/source":        fileDir(stepdef.FSRead, stepdef.FSWrite), // consumed files are moved or deleted
	"ftp/source":         remoteCapabilities(ftpProtocol),
	"ftps/source":        remoteCapabilities(ftpsProtocol),
	"sftp/source":        remoteCapabilities(sftpProtocol),
	"smb/source":         remoteCapabilities(smbProtocol),
	"googledrive/source": googleDriveCapabilities,
	"imaps/source":       imapsCapabilities,
	"https/source":       httpsSourceCapabilities,
	"rest/source":        restSourceCapabilities,
	"as2/source":         as2SourceCapabilities,

	// Actions that stay inside the process.
	"request/action":              none,
	"log/action":                  none,
	"logger/action":               none,
	"setbody/action":              none,
	"mapper/action":               none,
	"setheader/action":            none,
	"passthrough/action":          none,
	"setheaders/action":           none,
	"base64totext/action":         none,
	"base64tobinary/action":       none,
	"texttobase64/action":         none,
	"setbodyasstring/action":      none,
	"flowlink/action":             none,
	"service/action":              none,
	"queue/action":                none,
	"topic/action":                none,
	"removeheaders/action":        none,
	"replace/action":              none,
	"simplereplace/action":        none,
	"zip/action":                  none,
	"unzip/action":                none,
	"throttle/action":             none,
	"encoder/action":              none,
	"xmltojson/action":            none,
	"jsontoxml/action":            none,
	"xmltojsonsimple/action":      none,
	"jsontoxmlsimple/action":      none,
	"csvtoxml/action":             none,
	"xmltocsv/action":             none,
	"editoxml/action":             none,
	"xmltoedi/action":             none,
	"xmltoedifact/action":         none,
	"docconverter/action":         none,
	"formtoxml/action":            none,
	"flv/action":                  none,
	"exceltoxml/action":           none,
	"xmltoexcel/action":           none,
	"multipart/action":            none,
	"jsonvalidator/action":        none,
	"settenantvariable/action":    none,
	"gettenantvariable/action":    none,
	"removetenantvariable/action": none,
	"store/action":                none,
	"setcookie/action":            none,
	"removecookie/action":         none,
	"setoneway/action":            none,
	"setrequestreply/action":      none,
	"return/action":               none,
	"setuuid/action":              none,
	"setbodybyheader/action":      none,
	"setheaderbybody/action":      none,
	"delay/action":                none,
	"simplevalidator/action":      none,

	// Actions that read a file they are given (an XSLT stylesheet, a schema, a template).
	"xslt/action":         readsOption("path"),
	"velocity/action":     readsOption("path"),
	"xmlvalidator/action": readsOption("path"),
	"schematron/action":   readsOption("path"),
	"validate/action":     readsOption("schemaFile"),

	// Actions that reach outside.
	"https/action":       httpsActionCapabilities,
	"rest/action":        restActionCapabilities,
	"graphql/action":     graphqlCapabilities,
	"soap/action":        soapCapabilities,
	"smtp/action":        smtpCapabilities(false),
	"smtps/action":       smtpCapabilities(true),
	"googledrive/action": googleDriveCapabilities,
	"as2/action":         as2ActionCapabilities,
	"sql/action":         sqlCapabilities,
	"sql2/action":        sql2Capabilities,
	"fileenrich/action":  fileEnrichCapabilities,
	"ftpenrich/action":   remoteCapabilities(ftpProtocol),
	"ftpsenrich/action":  remoteCapabilities(ftpsProtocol),
	"sftpenrich/action":  remoteCapabilities(sftpProtocol),
	"smb/action":         remoteCapabilities(smbProtocol),
	"smbenrich/action":   remoteCapabilities(smbProtocol),

	// Routers.
	"wiretap/router":                        none,
	"recipient/router":                      none,
	"content/router":                        none,
	"if/router":                             none,
	"loop/router":                           none,
	"dowhile/router":                        none,
	"wastebin/router":                       none,
	"filter/router":                         none,
	"split/router":                          none,
	"splitwithnamespace/router":             none,
	"enrich/router":                         none,
	"aggregate/router":                      none,
	"idempotent/router":                     none,
	"splitandaggregate/router":              none,
	"splitandaggregatewithnamespace/router": none,

	// Sinks.
	"deadletter/sink":  none,
	"wastebin/sink":    none,
	"service/sink":     none,
	"reply/sink":       none,
	"file/sink":        fileDir(stepdef.FSWrite), // each file it writes is checked when it runs
	"ftp/sink":         remoteCapabilities(ftpProtocol),
	"ftps/sink":        remoteCapabilities(ftpsProtocol),
	"sftp/sink":        remoteCapabilities(sftpProtocol),
	"smb/sink":         remoteCapabilities(smbProtocol),
	"oauth2token/sink": oauth2TokenCapabilities,
}

// none declares a step that touches nothing outside the process.
func none(stepdef.Params) []stepdef.Capability { return []stepdef.Capability{} }

// capability is a capability of kind on target, the value of an option the
// step takes as it is. It is dynamic only when the value is encrypted
// (ENC(...)): that is decrypted when the step is built.
func capability(kind, target string) stepdef.Capability {
	return stepdef.Capability{Kind: kind, Target: target, Dynamic: strings.Contains(target, "ENC(")}
}

// template is a capability of kind on target, the value of an option the step
// evaluates for each message: dynamic when it has ${...} or @{...} in it, or
// is encrypted.
func template(kind, target string) stepdef.Capability {
	c := capability(kind, target)
	c.Dynamic = c.Dynamic || strings.Contains(target, "${") || strings.Contains(target, "@{")
	return c
}

// anyTarget is a capability of kind whose target is not known before the step runs.
func anyTarget(kind string) stepdef.Capability {
	return stepdef.Capability{Kind: kind, Target: "*", Dynamic: true}
}

// readsOption reads the file the option names, if it is set.
func readsOption(option string) func(stepdef.Params) []stepdef.Capability {
	return func(p stepdef.Params) []stepdef.Capability {
		if file := strings.TrimSpace(optString(p, option)); file != "" {
			return []stepdef.Capability{capability(stepdef.FSRead, file)}
		}
		return []stepdef.Capability{}
	}
}

// fileDir is a step on the directory of the option path.
func fileDir(kinds ...string) func(stepdef.Params) []stepdef.Capability {
	return func(p stepdef.Params) []stepdef.Capability {
		caps := []stepdef.Capability{}
		for _, k := range kinds {
			caps = append(caps, capability(k, optString(p, "path")))
		}
		return caps
	}
}

func fileEnrichCapabilities(p stepdef.Params) []stepdef.Capability {
	if p["delete"] == true {
		return fileDir(stepdef.FSRead, stepdef.FSWrite)(p)
	}
	return fileDir(stepdef.FSRead)(p)
}

// keystoreFile reads the PKCS#12 file of the option file, if it is set, with the
// password of the option password or, without that option, of the variable env.
func keystoreFile(p stepdef.Params, file string, password keystorePassword) []stepdef.Capability {
	f := strings.TrimSpace(optString(p, file))
	if f == "" {
		return nil
	}
	caps := []stepdef.Capability{capability(stepdef.FSRead, f)}
	if _, given := p[password.option].(string); !given {
		caps = append(caps, capability(stepdef.EnvVar, password.env))
	}
	return caps
}

// trustStoreFile reads the trust store of the options trustStoreFile and trustStorePassword.
func trustStoreFile(p stepdef.Params) []stepdef.Capability {
	return keystoreFile(p, "trustStoreFile", trustStorePassword)
}

// urlTarget is the host:port a URL (or //authority/path, or host[:port]/path)
// connects to: the port it names, else 443 for https, 80 for http, else port.
// For an address the step evaluates per message (expr), a host with ${...} in
// it is dynamic, with the placeholders, and an address that is all an
// expression is "*".
func urlTarget(raw, port string, expr bool) stepdef.Capability {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "://"); i > 0 && !strings.ContainsAny(s[:i], "{}") {
		switch strings.ToLower(s[:i]) {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
		s = s[i+3:]
	} else if rest, ok := strings.CutPrefix(s, "//"); ok {
		s = rest
	} else if expr && (strings.HasPrefix(s, "${") || strings.HasPrefix(s, "@{")) {
		return anyTarget(stepdef.NetConnect) // scheme and host come from the expression
	}
	// The authority ends at the first /, ? or # outside a placeholder.
	depth, end, at := 0, len(s), -1
	for i := 0; i < len(s) && end == len(s); i++ {
		switch c := s[i]; {
		case c == '{' && i > 0 && (s[i-1] == '$' || s[i-1] == '@'):
			depth++
		case c == '}' && depth > 0:
			depth--
		case depth == 0 && (c == '/' || c == '?' || c == '#'):
			end = i
		case depth == 0 && c == '@' && !strings.HasPrefix(s[i:], "@{"):
			at = i // userinfo is never shown
		}
	}
	authority := s[at+1 : end]
	if authority == "" {
		return anyTarget(stepdef.NetConnect)
	}
	if expr {
		return template(stepdef.NetConnect, withPort(authority, port))
	}
	return capability(stepdef.NetConnect, withPort(authority, port))
}

// withPort returns host:port, where authority is host or host:port.
func withPort(authority, port string) string {
	if _, _, err := net.SplitHostPort(authority); err == nil {
		return authority
	}
	return net.JoinHostPort(strings.Trim(authority, "[]"), port)
}

// listenAddress is the address a listener binds: host:port, with 0.0.0.0 for
// an empty host (all interfaces).
func listenAddress(host, port string) string {
	if host == "" {
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, port)
}

// listenTarget is listenAddress of addr, host:port.
func listenTarget(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return listenAddress(host, port)
}

// keyRef is what an AS2 key option that holds PEM text, the address of PEM or
// the path of a file reads.
func keyRef(p stepdef.Params, options ...string) []stepdef.Capability {
	var caps []stepdef.Capability
	for _, o := range options {
		ref := strings.TrimSpace(optString(p, o))
		switch {
		case ref == "" || strings.Contains(ref, "-----BEGIN"):
		case strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://"):
			caps = append(caps, urlTarget(ref, "443", false))
		default:
			caps = append(caps, capability(stepdef.FSRead, ref))
		}
	}
	return caps
}

func httpsActionCapabilities(p stepdef.Params) []stepdef.Capability {
	return append([]stepdef.Capability{urlTarget(optString(p, "path"), "443", true)}, trustStoreFile(p)...)
}

func restActionCapabilities(p stepdef.Params) []stepdef.Capability {
	target := strings.TrimSuffix(optString(p, "host"), "/") + "/" + strings.TrimPrefix(optString(p, "path"), "/")
	return append([]stepdef.Capability{urlTarget(target, "443", false)}, trustStoreFile(p)...)
}

func graphqlCapabilities(p stepdef.Params) []stepdef.Capability {
	raw := optString(p, "url")
	if raw == "" {
		raw = optString(p, "path")
	}
	return append([]stepdef.Capability{urlTarget(raw, "443", false)}, trustStoreFile(p)...)
}

// soapCapabilities: the WSDL comes from the same address as the calls.
func soapCapabilities(p stepdef.Params) []stepdef.Capability {
	return append([]stepdef.Capability{urlTarget(optString(p, "path"), "443", false)}, trustStoreFile(p)...)
}

func googleDriveCapabilities(p stepdef.Params) []stepdef.Capability {
	return append([]stepdef.Capability{urlTarget(googleDriveAPI, "443", false)}, trustStoreFile(p)...)
}

// smtpCapabilities: smtp:<host>:<port>; without a port, 587 (STARTTLS) or
// 465 (smtps). Without a password it reads DIF_SMTP_PASSWORD.
func smtpCapabilities(implicitTLS bool) func(stepdef.Params) []stepdef.Capability {
	port := "587"
	if implicitTLS {
		port = "465"
	}
	return func(p stepdef.Params) []stepdef.Capability {
		caps := []stepdef.Capability{capability(stepdef.NetConnect, withPort(strings.TrimPrefix(optString(p, "path"), "//"), port))}
		if optString(p, "password") == "" {
			caps = append(caps, capability(stepdef.EnvVar, "DIF_SMTP_PASSWORD"))
		}
		return append(caps, trustStoreFile(p)...)
	}
}

func imapsCapabilities(p stepdef.Params) []stepdef.Capability {
	caps := []stepdef.Capability{capability(stepdef.NetConnect, withPort(strings.TrimPrefix(optString(p, "path"), "//"), "993"))}
	if _, set := p["password"].(string); !set && p["authenticationType"] != "oauth" {
		caps = append(caps, capability(stepdef.EnvVar, "DIF_IMAPS_PASSWORD"))
	}
	return append(caps, trustStoreFile(p)...)
}

// remoteCapabilities: the server of the step's address, with the protocol's
// port by default. The passive data connections of ftp go to the same host,
// on a port the server chooses; they are not declared apart.
func remoteCapabilities(proto remoteProtocol) func(stepdef.Params) []stepdef.Capability {
	return func(p stepdef.Params) []stepdef.Capability {
		port := proto.port
		if proto.scheme == "ftps" && p["implicit"] == true {
			port = "990"
		}
		uri := optString(p, "path")
		if proto.scheme == "smb" {
			uri = strings.ReplaceAll(uri, `\`, "/")
		}
		server := anyTarget(stepdef.NetConnect)
		if u, err := parseRemoteURI(uri, port); err == nil {
			server = capability(stepdef.NetConnect, u.addr())
		}
		caps := []stepdef.Capability{server}
		if _, given := p["password"]; !given {
			caps = append(caps, capability(stepdef.EnvVar, "DIF_"+strings.ToUpper(proto.scheme)+"_PASSWORD"))
		}
		if proto.scheme == "sftp" {
			if key := rawOpt(p, "privateKey"); key != "" {
				caps = append(caps, capability(stepdef.FSRead, key))
				if _, given := p["privateKeyPassphrase"].(string); !given {
					caps = append(caps, capability(stepdef.EnvVar, "DIF_SFTP_PRIVATE_KEY_PASSPHRASE"))
				}
			}
			if p["strictHostKeyChecking"] == true {
				known := rawOpt(p, "knownHostsFile")
				if known == "" {
					if home, err := os.UserHomeDir(); err == nil {
						known = filepath.Join(home, ".ssh", "known_hosts")
					} else {
						known = filepath.Join("~", ".ssh", "known_hosts")
					}
				}
				caps = append(caps, capability(stepdef.FSRead, known))
			}
		}
		return append(caps, trustStoreFile(p)...)
	}
}

func httpsSourceCapabilities(p stepdef.Params) []stepdef.Capability {
	listen := anyTarget(stepdef.NetListen)
	if addr, _, err := splitAddress(optString(p, "path")); err == nil {
		listen = capability(stepdef.NetListen, listenTarget(addr))
	}
	return append([]stepdef.Capability{listen}, keystoreFile(p, "serverIdentityFile", serverIdentityPassword)...)
}

func restSourceCapabilities(p stepdef.Params) []stepdef.Capability {
	caps := []stepdef.Capability{capability(stepdef.NetListen, listenTarget(optString(p, "address")))}
	return append(caps, keystoreFile(p, "serverIdentityFile", serverIdentityPassword)...)
}

func as2SourceCapabilities(p stepdef.Params) []stepdef.Capability {
	port, _ := p["serverPortNumber"].(int)
	caps := []stepdef.Capability{capability(stepdef.NetListen, listenAddress(optString(p, "address"), strconv.Itoa(port)))}
	caps = append(caps, keystoreFile(p, "serverIdentityFile", serverIdentityPassword)...)
	caps = append(caps, keyRef(p, "decryptingPrivateKey", "signingPrivateKey", "signingCertificateChain", "validateSigningCertificateChain")...)
	return append(caps, trustStoreFile(p)...)
}

// as2ActionCapabilities: the partner's host and port, as newAS2Action makes
// the address: https on 443, 8443 or with an sslContext, else http.
func as2ActionCapabilities(p stepdef.Params) []stepdef.Capability {
	host := strings.TrimSpace(optString(p, "hostName"))
	port := strings.TrimSpace(optString(p, "targetPortNumber"))
	if port == "" {
		port = strings.TrimSpace(optString(p, "port"))
	}
	if port == "" {
		port = "80"
		if optString(p, "sslContext") != "" {
			port = "443"
		}
	}
	server := anyTarget(stepdef.NetConnect)
	if host != "" {
		server = capability(stepdef.NetConnect, net.JoinHostPort(strings.Trim(host, "[]"), port))
	}
	caps := append([]stepdef.Capability{server}, keyRef(p, "certificateForSigning", "certificateForEncrypt")...)
	return append(caps, trustStoreFile(p)...)
}

// sqlCapabilities: the database of the options host and port (templates, so
// they may be dynamic), with the default port of connectionType.
func sqlCapabilities(p stepdef.Params) []stepdef.Capability {
	caps := []stepdef.Capability{sqlServer(optString(p, "connectionType"), optString(p, "host"), optString(p, "port"), template)}
	if _, set := p["password"].(string); !set {
		caps = append(caps, capability(stepdef.EnvVar, "DIF_SQL_PASSWORD"))
	}
	return append(caps, sqlTrustStore(p)...)
}

// sql2Capabilities: the database of the connection the loader gave the step.
func sql2Capabilities(p stepdef.Params) []stepdef.Capability {
	var k sqlConnectionKeys
	server := anyTarget(stepdef.NetConnect)
	if json.Unmarshal([]byte(optString(p, "connection")), &k) == nil && k.Host != "" {
		port := ""
		switch x := k.Port.(type) {
		case string:
			port = x
		case float64:
			port = strconv.Itoa(int(x))
		}
		server = sqlServer(k.DBType, k.Host, port, capability)
	}
	return append([]stepdef.Capability{server}, sqlTrustStore(p)...)
}

// sqlServer is the database at host and port, with the dialect's port by
// default; as makes the capability (template where they are templates).
func sqlServer(dialect, host, port string, as func(kind, target string) stepdef.Capability) stepdef.Capability {
	host, port = strings.TrimSpace(host), strings.TrimSpace(port)
	if host == "" {
		return anyTarget(stepdef.NetConnect)
	}
	if port == "" {
		port = "*"
		if d, err := sqlDialectFor(dialect); err == nil {
			port = strconv.Itoa(d.defaultPort)
		}
	}
	return as(stepdef.NetConnect, net.JoinHostPort(host, port))
}

func sqlTrustStore(p stepdef.Params) []stepdef.Capability {
	if p["useSSL"] == true {
		return trustStoreFile(p)
	}
	return nil
}

// oauth2TokenCapabilities: the token endpoint of tokenUrl, or, when the flow
// does not name it, of the environment (dynamic: known when the step is built),
// and the variables of the settings the flow does not give (see oauth2Setting).
func oauth2TokenCapabilities(p stepdef.Params) []stepdef.Capability {
	var tokens []string
	for _, name := range strings.Split(optString(p, "tokenName"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			tokens = append(tokens, name)
		}
	}
	endpoint := anyTarget(stepdef.NetConnect)
	if u := optString(p, "tokenUrl"); u != "" {
		endpoint = urlTarget(u, "443", false)
	}
	caps := []stepdef.Capability{endpoint}
	for _, s := range []struct{ option, setting string }{
		{"tokenUrl", "TOKEN_URL"}, {"clientId", "CLIENT_ID"}, {"clientSecret", "CLIENT_SECRET"},
		{"refreshToken", "REFRESH_TOKEN"}, {"scope", "SCOPE"},
	} {
		if v, ok := p[s.option].(string); ok && (v != "" || s.option == "clientSecret" || s.option == "refreshToken") {
			continue
		}
		for _, t := range tokens {
			caps = append(caps, capability(stepdef.EnvVar, oauth2Env(t, s.setting)))
		}
		caps = append(caps, capability(stepdef.EnvVar, "DIF_OAUTH2_"+s.setting))
	}
	return append(caps, trustStoreFile(p)...)
}
