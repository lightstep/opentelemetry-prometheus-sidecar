package snappy

import (
	"bytes"
	"io"
	"io/ioutil"
	"sync"

	"github.com/golang/snappy"
	"google.golang.org/grpc/encoding"
)

// Name is the name registered for the snappy compressor.
const Name = "snappy"

type compressor struct {
	writerPool sync.Pool
	readerPool sync.Pool
}

func init() {
	c := &compressor{}
	c.writerPool.New = func() interface{} {
		return &writer{Writer: snappy.NewBufferedWriter(ioutil.Discard), pool: &c.writerPool}
	}
	c.readerPool.New = func() interface{} {
		return &reader{Reader: snappy.NewReader(bytes.NewReader(nil)), pool: &c.readerPool}
	}
	encoding.RegisterCompressor(c)
}

type writer struct {
	*snappy.Writer
	pool *sync.Pool
}

func (c *compressor) Compress(w io.Writer) (io.WriteCloser, error) {
	z := c.writerPool.Get().(*writer)
	z.Reset(w)
	return z, nil
}

func (w *writer) Close() error {
	defer w.pool.Put(w)
	return w.Writer.Close()
}

type reader struct {
	*snappy.Reader
	pool *sync.Pool
}

func (c *compressor) Decompress(r io.Reader) (io.Reader, error) {
	z := c.readerPool.Get().(*reader)
	z.Reader.Reset(r)
	return z, nil
}

func (r *reader) Read(p []byte) (n int, err error) {
	n, err = r.Reader.Read(p)
	if err == io.EOF {
		r.pool.Put(r)
	}
	return n, err
}

func (c *compressor) Name() string {
	return Name
}
