package server

import (
	"bytes"
	"fmt"
	"github.com/siddontang/ledisdb/ledis"
	"io"
	"time"
)

var txUnsupportedCmds = map[string]struct{}{
	"select":   struct{}{},
	"slaveof":  struct{}{},
	"fullsync": struct{}{},
	"sync":     struct{}{},
	"begin":    struct{}{},
	"flushall": struct{}{},
	"flushdb":  struct{}{},
	"eval":     struct{}{},
}

var scriptUnsupportedCmds = map[string]struct{}{
	"slaveof":  struct{}{},
	"fullsync": struct{}{},
	"sync":     struct{}{},
	"begin":    struct{}{},
	"commit":   struct{}{},
	"rollback": struct{}{},
	"flushall": struct{}{},
	"flushdb":  struct{}{},
}

type responseWriter interface {
	writeError(error)
	writeStatus(string)
	writeInteger(int64)
	writeBulk([]byte)
	writeArray([]interface{})
	writeSliceArray([][]byte)
	writeFVPairArray([]ledis.FVPair)
	writeScorePairArray([]ledis.ScorePair, bool)
	writeBulkFrom(int64, io.Reader)
	flush()
}

type syncAck struct {
	id uint64
	ch chan uint64
}

type client struct {
	app *App
	ldb *ledis.Ledis

	db *ledis.DB

	remoteAddr string
	cmd        string
	args       [][]byte

	resp responseWriter

	syncBuf bytes.Buffer

	lastLogID uint64

	ack *syncAck

	reqErr chan error

	buf bytes.Buffer

	tx *ledis.Tx

	script *ledis.Multi
}

func newClient(app *App) *client {
	c := new(client)

	c.app = app
	c.ldb = app.ldb
	c.db, _ = app.ldb.Select(0) //use default db

	c.reqErr = make(chan error)

	return c
}

func (c *client) perform() {
	var err error

	start := time.Now()

	if len(c.cmd) == 0 {
		err = ErrEmptyCommand
	} else if exeCmd, ok := regCmds[c.cmd]; !ok {
		err = ErrNotFound
	} else {
		if c.db.IsTransaction() {
			if _, ok := txUnsupportedCmds[c.cmd]; ok {
				err = fmt.Errorf("%s not supported in transaction", c.cmd)
			}
		} else if c.db.IsInMulti() {
			if _, ok := scriptUnsupportedCmds[c.cmd]; ok {
				err = fmt.Errorf("%s not supported in multi", c.cmd)
			}
		}

		if err == nil {
			go func() {
				c.reqErr <- exeCmd(c)
			}()

			err = <-c.reqErr
		}
	}

	duration := time.Since(start)

	if c.app.access != nil {
		fullCmd := c.catGenericCommand()
		cost := duration.Nanoseconds() / 1000000

		truncateLen := len(fullCmd)
		if truncateLen > 256 {
			truncateLen = 256
		}

		c.app.access.Log(c.remoteAddr, cost, fullCmd[:truncateLen], err)
	}

	if err != nil {
		c.resp.writeError(err)
	}
	c.resp.flush()
	return
}

func (c *client) catGenericCommand() []byte {
	buffer := c.buf
	buffer.Reset()

	buffer.Write([]byte(c.cmd))

	for _, arg := range c.args {
		buffer.WriteByte(' ')
		buffer.Write(arg)
	}

	return buffer.Bytes()
}

func writeValue(w responseWriter, value interface{}) {
	switch v := value.(type) {
	case []interface{}:
		w.writeArray(v)
	case [][]byte:
		w.writeSliceArray(v)
	case []byte:
		w.writeBulk(v)
	case string:
		w.writeStatus(v)
	case nil:
		w.writeBulk(nil)
	case int64:
		w.writeInteger(v)
	default:
		panic("invalid value type")
	}
}
