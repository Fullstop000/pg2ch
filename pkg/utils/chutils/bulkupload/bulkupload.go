package bulkupload

import (
	"compress/gzip"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"gopkg.in/djherbis/buffer.v1"
	"gopkg.in/djherbis/nio.v2"

	"github.com/mkabilov/pg2ch/pkg/config"
	"github.com/mkabilov/pg2ch/pkg/utils/chutils"
)

var (
	bufPool = sync.Pool{
		New: func() interface{} {
			return buffer.New(1 * 1024 * 1024)
		}}

	client = &http.Client{}
)

type BulkUploader interface {
	Start() error
	Finish() error
	Write(p []byte) error
	BulkUpload(name config.ChTableName, columns []string) error
}

type BulkUpload struct {
	baseURL      string
	pipeWriter   *nio.PipeWriter
	pipeReader   *nio.PipeReader
	gzipWriter   *gzip.Writer
	buf          buffer.Buffer
	tableName    string
	columns      []string
	gzipBufBytes int
	gzipBufSize  int
}

func New(baseURL string, gzipBufSize int) *BulkUpload {
	ch := &BulkUpload{
		baseURL:     strings.TrimRight(baseURL, "/") + "/",
		gzipBufSize: gzipBufSize,
	}

	return ch
}

func (c *BulkUpload) performRequest(query string, body io.Reader) error {
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"?query="+url.QueryEscape(query), body)
	if err != nil {
		return fmt.Errorf("could not create request: %v", err)
	}
	req.Header.Add("Content-Encoding", "gzip")
	req.Header.Set("User-Agent", config.ApplicationName)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("could not perform request: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("could not close body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("could not read err body: %v", err)
		}

		return fmt.Errorf("got %d status code from clickhouse: %s", resp.StatusCode, string(body))
	}

	return nil
}

func (c *BulkUpload) BulkUpload(tableName config.ChTableName, columns []string) error {
	defer func() {
		c.buf.Reset()
		bufPool.Put(c.buf)
	}()

	if err := c.performRequest(chutils.GenInsertQuery(tableName, columns), c.pipeReader); err != nil {
		return err
	}

	return nil
}

func (c *BulkUpload) Start() error {
	var err error

	c.buf = bufPool.Get().(buffer.Buffer)
	c.pipeReader, c.pipeWriter = nio.Pipe(c.buf)
	c.gzipWriter, err = gzip.NewWriterLevel(c.pipeWriter, gzip.BestSpeed) // TODO: move gzip level to config
	if err != nil {
		return err
	}

	return nil
}

func (c *BulkUpload) Write(p []byte) error {
	c.gzipBufBytes += len(p)

	_, err := c.gzipWriter.Write(p)

	if c.gzipBufBytes >= c.gzipBufSize {
		if err := c.gzipWriter.Flush(); err != nil {
			return fmt.Errorf("could not flush gzip: %v", err)
		}
		c.gzipBufBytes = 0
	}

	return err
}

func (c *BulkUpload) Finish() error {
	if err := c.gzipWriter.Close(); err != nil {
		return err
	}

	return c.pipeWriter.Close()
}
