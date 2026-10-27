package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// S3Store keeps backups in any S3-compatible object store (AWS S3, Cloudflare R2,
// Backblaze B2, MinIO). It signs requests with AWS Signature V4 by hand, so it
// pulls in no SDK and keeps the orchestrator dependency-free. Path-style
// addressing (endpoint/bucket/key) is used for the widest compatibility.
//
// Objects are keyed <prefix>/<workloadID>/<timestamp>[.<kind>].tar.gz, mirroring LocalStore.
type S3Store struct {
	endpoint  string // e.g. https://<acct>.r2.cloudflarestorage.com or http://127.0.0.1:9000
	bucket    string
	region    string
	prefix    string
	accessKey string
	secretKey string
	client    *http.Client
}

func NewS3Store(endpoint, bucket, region, prefix, accessKey, secretKey string) (*S3Store, error) {
	if endpoint == "" || bucket == "" || accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("s3: endpoint, bucket, access key and secret key are required")
	}
	if region == "" {
		region = "auto" // R2 uses "auto"
	}
	if prefix == "" {
		prefix = "backups"
	}
	return &S3Store{
		endpoint:  strings.TrimRight(endpoint, "/"),
		bucket:    bucket,
		region:    region,
		prefix:    strings.Trim(prefix, "/"),
		accessKey: accessKey,
		secretKey: secretKey,
		client:    &http.Client{Timeout: 5 * time.Minute},
	}, nil
}

func (s *S3Store) key(workloadID string, ts time.Time, kind string) string {
	return s.prefix + "/" + workloadID + "/" + fileName(ts, kind)
}

func (s *S3Store) objURL(key string) string {
	return s.endpoint + "/" + s.bucket + "/" + key
}

func (s *S3Store) Create(workloadID, dataDir string, exclude []string) (Backup, error) {
	// Tar to a temp file so we can hash it (SigV4 needs the payload sha256) and
	// upload it with a known length.
	tmp, err := os.CreateTemp("", "tbbackup-*.tar.gz")
	if err != nil {
		return Backup{}, err
	}
	defer os.Remove(tmp.Name())
	err = writeTarGz(tmp, dataDir, exclude)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Backup{}, err
	}
	return s.Put(workloadID, tmp.Name(), KindDir)
}

func (s *S3Store) Put(workloadID, file, kind string) (Backup, error) {
	now := time.Now().UTC()
	f, err := os.Open(file)
	if err != nil {
		return Backup{}, err
	}
	defer f.Close()
	sum := sha256.New()
	size, err := io.Copy(sum, f)
	if err != nil {
		return Backup{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Backup{}, err
	}

	req, _ := http.NewRequest(http.MethodPut, s.objURL(s.key(workloadID, now, kind)), f)
	req.ContentLength = size
	s.sign(req, hex.EncodeToString(sum.Sum(nil)), now)
	resp, err := s.client.Do(req)
	if err != nil {
		return Backup{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return Backup{}, s3err("put", resp)
	}
	return Backup{ID: backupID(workloadID, now, kind), WorkloadID: workloadID, CreatedAt: now, SizeBytes: size, Kind: kind}, nil
}

type listResult struct {
	Contents []struct {
		Key  string `xml:"Key"`
		Size int64  `xml:"Size"`
	} `xml:"Contents"`
	IsTruncated bool   `xml:"IsTruncated"`
	NextToken   string `xml:"NextContinuationToken"`
}

func (s *S3Store) List(workloadID string) ([]Backup, error) {
	prefix := s.prefix + "/"
	if workloadID != "" {
		prefix += workloadID + "/"
	}
	var out []Backup
	token := ""
	for {
		q := url.Values{}
		q.Set("list-type", "2")
		q.Set("prefix", prefix)
		if token != "" {
			q.Set("continuation-token", token)
		}
		u := s.endpoint + "/" + s.bucket + "?" + q.Encode()
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		s.sign(req, emptyHash, time.Now().UTC())
		resp, err := s.client.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("s3 list: %s: %s", resp.Status, strings.TrimSpace(string(body)))
		}
		var lr listResult
		if err := xml.Unmarshal(body, &lr); err != nil {
			return nil, err
		}
		for _, c := range lr.Contents {
			parts := strings.Split(c.Key, "/")
			if len(parts) < 2 {
				continue
			}
			ts, kind, ok := parseFileName(parts[len(parts)-1])
			if !ok {
				continue
			}
			wid := parts[len(parts)-2] // workloadID = second-to-last path segment
			out = append(out, Backup{ID: backupID(wid, ts, kind), WorkloadID: wid, CreatedAt: ts, SizeBytes: c.Size, Kind: kind})
		}
		if !lr.IsTruncated || lr.NextToken == "" {
			break
		}
		token = lr.NextToken
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// get opens a backup's object for reading; the caller closes the body.
func (s *S3Store) get(id string) (io.ReadCloser, error) {
	wid, ts, kind, err := parseID(id)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequest(http.MethodGet, s.objURL(s.key(wid, ts, kind)), nil)
	s.sign(req, emptyHash, time.Now().UTC())
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, s3err("get", resp)
	}
	return resp.Body, nil
}

func (s *S3Store) Restore(id, destDir string) error {
	if _, _, kind, err := parseID(id); err == nil && kind != KindDir {
		return ErrNotExtractable
	}
	body, err := s.get(id)
	if err != nil {
		return err
	}
	defer body.Close()
	return extractTarGz(body, destDir)
}

func (s *S3Store) Fetch(id, destFile string) error {
	body, err := s.get(id)
	if err != nil {
		return err
	}
	defer body.Close()
	out, err := os.Create(destFile)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, body); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func (s *S3Store) Delete(id string) error {
	wid, ts, kind, err := parseID(id)
	if err != nil {
		return err
	}
	req, _ := http.NewRequest(http.MethodDelete, s.objURL(s.key(wid, ts, kind)), nil)
	s.sign(req, emptyHash, time.Now().UTC())
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return s3err("delete", resp)
	}
	return nil
}

func (s *S3Store) Retain(workloadID string, keep int) error {
	list, err := s.List(workloadID)
	if err != nil {
		return err
	}
	for i := keep; i < len(list); i++ {
		_ = s.Delete(list[i].ID)
	}
	return nil
}

func s3err(op string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("s3 %s: %s: %s", op, resp.Status, strings.TrimSpace(string(body)))
}

const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
