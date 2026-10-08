package impl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// The googledrive steps use a Google Drive folder like a directory: the
// source consumes the files in it, the action writes the body to a file in it.
// They call the Drive v3 REST API with an access token, which the option
// accessToken names, usually as the tenant variable @{...} an oauth2token
// step keeps fresh.

// googleDriveAPI is the base URL of the Drive API; tests point it elsewhere.
var googleDriveAPI = "https://www.googleapis.com"

// driveNative is the MIME type prefix of Google's own formats (Docs, Sheets,
// folders, ...), which have no content to download.
const driveNative = "application/vnd.google-apps."

// DriveID is the header the googledrive steps set to the id of the file.
const DriveID = "googledrive.id"

type driveFile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
}

// driveFolder is the folder the steps work in, and how to call the API for it.
type driveFolder struct {
	id     string
	token  string // accessToken option, which may hold @{variable}
	tenant string // tenant of the variables
	client *http.Client
}

func newDriveFolder(p stepdef.Params) (driveFolder, error) {
	f := driveFolder{id: p["path"].(string), tenant: p["tenant"].(string), token: p["accessToken"].(string)}
	if f.token == "" {
		return f, fmt.Errorf("option accessToken: empty")
	}
	var err error
	f.client, err = httpsClient(p)
	return f, err
}

// driveQuote quotes s as a string in a Drive search query.
func driveQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// call sends a request to the API and returns the response body. A body of
// more than maxBodySize is an error rather than silently cut short.
func (f driveFolder) call(ctx context.Context, method, path string, q url.Values, contentType string, body []byte) ([]byte, error) {
	token, err := expandTenantVariables(f.tenant, f.token)
	if err != nil {
		return nil, fmt.Errorf("access token: %w", err)
	}
	if token == "" {
		return nil, fmt.Errorf("access token is empty")
	}
	q.Set("supportsAllDrives", "true")
	req, err := http.NewRequestWithContext(ctx, method, googleDriveAPI+path+"?"+q.Encode(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize+1))
	if err != nil {
		return nil, fmt.Errorf("%s %s: reading response: %w", method, path, err)
	}
	if len(data) > maxBodySize {
		return nil, fmt.Errorf("%s %s: response is larger than %d bytes", method, path, maxBodySize)
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct{ Message string }
		}
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			return nil, fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, e.Error.Message)
		}
		return nil, fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	return data, nil
}

// list returns the files that match the search query, sorted by name.
func (f driveFolder) list(ctx context.Context, query string) ([]driveFile, error) {
	var files []driveFile
	q := url.Values{
		"q":                         {query},
		"orderBy":                   {"name"},
		"pageSize":                  {"100"},
		"fields":                    {"nextPageToken,files(id,name,mimeType)"},
		"includeItemsFromAllDrives": {"true"},
	}
	for {
		data, err := f.call(ctx, http.MethodGet, "/drive/v3/files", q, "", nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Files         []driveFile `json:"files"`
			NextPageToken string      `json:"nextPageToken"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("list files: %w", err)
		}
		files = append(files, page.Files...)
		if page.NextPageToken == "" {
			return files, nil
		}
		q.Set("pageToken", page.NextPageToken)
	}
}

// inFolder is the search query for the entries of the folder, by name if
// name is not empty.
func (f driveFolder) inFolder(name string) string {
	q := driveQuote(f.id) + " in parents and trashed = false"
	if name != "" {
		q += " and name = " + driveQuote(name)
	}
	return q
}

// find returns the file or folder called name in the folder, or nil.
func (f driveFolder) find(ctx context.Context, name string) (*driveFile, error) {
	files, err := f.list(ctx, f.inFolder(name))
	if err != nil || len(files) == 0 {
		return nil, err
	}
	return &files[0], nil
}

// download returns the content of the file.
func (f driveFolder) download(ctx context.Context, id string) ([]byte, error) {
	return f.call(ctx, http.MethodGet, "/drive/v3/files/"+url.PathEscape(id), url.Values{"alt": {"media"}}, "", nil)
}

// subfolder returns the id of the subfolder called name, creating it if needed.
func (f driveFolder) subfolder(ctx context.Context, name string) (string, error) {
	found, err := f.list(ctx, f.inFolder(name)+" and mimeType = "+driveQuote(driveNative+"folder"))
	if err != nil {
		return "", err
	}
	if len(found) > 0 {
		return found[0].ID, nil
	}
	meta, _ := json.Marshal(map[string]any{"name": name, "mimeType": driveNative + "folder", "parents": []string{f.id}})
	data, err := f.call(ctx, http.MethodPost, "/drive/v3/files", url.Values{"fields": {"id"}}, "application/json", meta)
	if err != nil {
		return "", err
	}
	var created driveFile
	if err := json.Unmarshal(data, &created); err != nil || created.ID == "" {
		return "", fmt.Errorf("create folder %s: no id in the response", name)
	}
	return created.ID, nil
}

// move moves the file from the folder to the folder to.
func (f driveFolder) move(ctx context.Context, id, to string) error {
	q := url.Values{"addParents": {to}, "removeParents": {f.id}, "fields": {"id"}}
	_, err := f.call(ctx, http.MethodPatch, "/drive/v3/files/"+url.PathEscape(id), q, "application/json", []byte("{}"))
	return err
}

// create uploads a new file to the folder and returns its id.
func (f driveFolder) create(ctx context.Context, name, contentType string, content []byte) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	meta, _ := json.Marshal(map[string]any{"name": name, "parents": []string{f.id}})
	for _, part := range []struct {
		contentType string
		data        []byte
	}{{"application/json; charset=UTF-8", meta}, {contentType, content}} {
		pw, err := w.CreatePart(textproto.MIMEHeader{"Content-Type": {part.contentType}})
		if err != nil {
			return "", err
		}
		pw.Write(part.data)
	}
	w.Close()
	q := url.Values{"uploadType": {"multipart"}, "fields": {"id"}}
	data, err := f.call(ctx, http.MethodPost, "/upload/drive/v3/files", q, "multipart/related; boundary="+w.Boundary(), body.Bytes())
	return idOf(data, err)
}

// replace replaces the content of the file.
func (f driveFolder) replace(ctx context.Context, id, contentType string, content []byte) (string, error) {
	q := url.Values{"uploadType": {"media"}, "fields": {"id"}}
	data, err := f.call(ctx, http.MethodPatch, "/upload/drive/v3/files/"+url.PathEscape(id), q, contentType, content)
	return idOf(data, err)
}

func idOf(data []byte, err error) (string, error) {
	if err != nil {
		return "", err
	}
	var f driveFile
	if err := json.Unmarshal(data, &f); err != nil || f.ID == "" {
		return "", fmt.Errorf("upload: no file id in the response")
	}
	return f.ID, nil
}

// driveSource polls the folder and emits a message per file, with the file's
// content as body, the header file.name and googledrive.id. Google's own
// formats and subfolders are skipped. A consumed file is moved to the
// subfolder moveTo. A poll that fails, such as one before the token exists, is
// logged and tried again; if moving a file fails, it is consumed again.
type driveSource struct {
	driveFolder
	fileName     string // consume only files with this name; "" for all
	moveTo       string
	initialDelay time.Duration
	delay        time.Duration
}

func newDriveSource(_ string, p stepdef.Params) (stepdef.Processor, error) {
	f, err := newDriveFolder(p)
	if err != nil {
		return nil, err
	}
	s := driveSource{
		driveFolder:  f,
		moveTo:       p["moveTo"].(string),
		initialDelay: time.Duration(p["initialDelay"].(int)) * time.Millisecond,
		delay:        time.Duration(p["delay"].(int)) * time.Millisecond,
	}
	s.fileName, _ = p["filterFiles"].(string)
	return s, nil
}

func (s driveSource) Run(ctx context.Context, emit stepdef.Emit) error {
	return s.RunReady(ctx, emit, func() {})
}

func (s driveSource) RunReady(ctx context.Context, emit stepdef.Emit, ready func()) error {
	ready()
	for wait := s.initialDelay; ; wait = s.delay {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		stopping, err := s.poll(ctx, emit)
		if stopping || ctx.Err() != nil {
			return nil
		}
		if err != nil {
			stepdef.Logger(ctx).Printf("googledrive %s: %v", s.id, err)
		}
	}
}

// poll consumes the files in the folder. It reports whether the flow is stopping.
func (s driveSource) poll(ctx context.Context, emit stepdef.Emit) (stopping bool, err error) {
	files, err := s.list(ctx, s.inFolder(s.fileName))
	if err != nil {
		return false, err
	}
	var done string // the id of the moveTo folder, once needed
	for _, file := range files {
		if strings.HasPrefix(file.MimeType, driveNative) {
			continue
		}
		data, err := s.download(ctx, file.ID)
		if err != nil {
			return false, err
		}
		m := message.New(string(data))
		m[FileName] = file.Name
		m[DriveID] = file.ID
		setContentType(m, file.Name)
		if emit(m, nil) != nil {
			return true, nil // the file stays for the next run
		}
		if done == "" {
			if done, err = s.subfolder(ctx, s.moveTo); err != nil {
				return false, err
			}
		}
		if err := s.move(ctx, file.ID, done); err != nil {
			return false, err
		}
	}
	return false, nil
}

// driveAction writes the body to a file in the folder and sets the header
// googledrive.id to its id. The file name is the fileName option, else the
// header file.name (FileName), else CamelFileName.
type driveAction struct {
	driveFolder
	fileName  string
	fileExist string // Override, Fail or Ignore
}

func newDriveAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	f, err := newDriveFolder(p)
	if err != nil {
		return nil, err
	}
	a := driveAction{driveFolder: f, fileExist: p["fileExist"].(string)}
	a.fileName, _ = p["fileName"].(string)
	return a, nil
}

func (a driveAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	name := a.fileName
	for _, h := range []string{FileName, "CamelFileName"} {
		if s, _ := m[h].(string); name == "" {
			name = s
		}
	}
	if name == "" {
		return nil, fmt.Errorf("googledrive: no file name: set the fileName option or the %s header", FileName)
	}
	contentType, _ := m[message.ContentType].(string)
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	existing, err := a.find(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("googledrive: %w", err)
	}
	var id string
	switch {
	case existing == nil:
		id, err = a.create(ctx, name, contentType, bytesOf(m[message.Body]))
	case a.fileExist == "Fail":
		return nil, fmt.Errorf("googledrive: file %s already exists", name)
	case a.fileExist == "Ignore":
		return m, nil
	default:
		id, err = a.replace(ctx, existing.ID, contentType, bytesOf(m[message.Body]))
	}
	if err != nil {
		return nil, fmt.Errorf("googledrive: %w", err)
	}
	m[DriveID] = id
	return m, nil
}
