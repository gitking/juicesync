// Copyright (C) 2018-present Juicedata Inc.

package object

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/qiniu/api.v7/kodo"
	"golang.org/x/net/context"
)

type qiniu struct {
	s3client
	b      *kodo.Bucket
	marker string
}

func (q *qiniu) String() string {
	return fmt.Sprintf("qiniu://%s", q.bucket)
}

func (q *qiniu) download(key string, off, limit int64) (io.ReadCloser, error) {
	if os.Getenv("QINIU_DOMAIN") == "" {
		return nil, errors.New("Please export QINIU_DOMAIN to download keys with prefix '/'")
	}
	baseUrl := kodo.MakeBaseUrl(os.Getenv("QINIU_DOMAIN"), key)
	url := q.b.Conn.MakePrivateUrl(baseUrl, nil)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(http.TimeFormat)
	req.Header.Add("Date", now)
	if off > 0 || limit > 0 {
		if limit > 0 {
			req.Header.Add("Range", fmt.Sprintf("bytes=%d-%d", off, off+limit-1))
		} else {
			req.Header.Add("Range", fmt.Sprintf("bytes=%d-", off))
		}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		return nil, fmt.Errorf("Status code: %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (q *qiniu) Get(key string, off, limit int64) (io.ReadCloser, error) {
	// S3 SDK cannot get objects with prefix "/" in the key
	if strings.HasPrefix(key, "/") {
		return q.download(key, off, limit)
	}
	return q.s3client.Get(key, off, limit)
}

func (q *qiniu) Put(key string, in io.Reader) error {
	body, vlen, err := findLen(in)
	if err != nil {
		return err
	}
	var ret kodo.PutRet
	return q.b.Put(context.Background(), &ret, key, body, vlen, nil)
}

func (q *qiniu) CreateMultipartUpload(key string) (*MultipartUpload, error) {
	return nil, notSupported
}

func (q *qiniu) Copy(dst, src string) error {
	return q.b.Copy(context.Background(), src, dst)
}

func (q *qiniu) Exists(key string) error {
	_, err := q.b.Stat(context.Background(), key)
	return err
}

func (q *qiniu) Delete(key string) error {
	if err := q.Exists(key); err != nil {
		return err
	}
	return q.b.Delete(context.Background(), key)
}

func (q *qiniu) List(prefix, marker string, limit int64) ([]*Object, error) {
	if marker == "" {
		q.marker = ""
	} else if q.marker == "" {
		// last page
		return nil, nil
	}
	entries, _, markerOut, err := q.b.List(context.Background(), prefix, "", q.marker, int(limit))
	q.marker = markerOut
	if len(entries) > 0 || err == io.EOF {
		// ignore error if returned something
		err = nil
	}
	if err != nil {
		return nil, err
	}
	n := len(entries)
	objs := make([]*Object, n)
	for i := 0; i < n; i++ {
		entry := entries[i]
		mtime := int(entry.PutTime / 10000000)
		objs[i] = &Object{entry.Key, entry.Fsize, mtime, mtime}
	}
	return objs, nil
}

var regions = map[string]int{
	"cn-east-1":  0,
	"cn-north-1": 1,
	"cn-south-1": 2,
	"us-west-1":  3,
}

func newQiniu(endpoint, accessKey, secretKey string) ObjectStorage {
	uri, err := url.ParseRequestURI(endpoint)
	if err != nil {
		logger.Fatalf("Invalid endpoint: %v, error: %v", endpoint, err)
	}
	hostParts := strings.SplitN(uri.Host, ".", 2)
	bucket := hostParts[0]
	endpoint = hostParts[1]
	region := endpoint[:strings.LastIndex(endpoint, "-")]
	awsConfig := &aws.Config{
		Credentials:      credentials.NewStaticCredentials(accessKey, secretKey, ""),
		Endpoint:         &endpoint,
		Region:           &region,
		DisableSSL:       aws.Bool(false),
		S3ForcePathStyle: aws.Bool(true),
		HTTPClient:       httpClient,
	}

	sess := session.New(awsConfig)
	s3client := s3client{bucket, s3.New(sess), sess}

	zone := regions[region]
	kodo.SetMac(accessKey, secretKey)
	c := kodo.New(zone, nil)
	b := c.Bucket(bucket)
	return &qiniu{s3client, &b, ""}
}

func init() {
	register("qiniu", newQiniu)
}
