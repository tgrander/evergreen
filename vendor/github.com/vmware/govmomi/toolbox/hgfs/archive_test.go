/*
Copyright (c) 2017 VMware, Inc. All Rights Reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package hgfs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"strings"
	"testing"
	"time"
)

func TestReadArchive(t *testing.T) {
	Trace = testing.Verbose()

	dir, err := ioutil.TempDir("", "toolbox-")
	if err != nil {
		t.Fatal(err)
	}

	nfiles := 5
	subdir := path.Join(dir, "hangar-18")
	_ = os.MkdirAll(subdir, 0755)
	dirs := []string{dir, subdir}

	for i := 0; i < nfiles; i++ {
		for _, p := range dirs {
			data := bytes.NewBufferString(strings.Repeat("X", i+1024))

			f, ferr := ioutil.TempFile(p, fmt.Sprintf("file-%d-", i))
			if ferr != nil {
				t.Fatal(ferr)
			}
			_, err = io.Copy(f, data)
			if err != nil {
				t.Fatal(err)
			}
			err = f.Close()
			if err != nil {
				t.Fatal(err)
			}

			if i == 0 {
				err = os.Symlink(f.Name(), path.Join(p, "first-file"))
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	}

	c := NewClient()
	c.s.Archive = true

	status := c.CreateSession()
	if status != StatusSuccess {
		t.Fatalf("status=%d", status)
	}

	_, status = c.GetAttr(dir)
	if status != StatusSuccess {
		t.Errorf("status=%d", status)
	}

	handle, status := c.Open(dir)
	if status != StatusSuccess {
		t.Fatalf("status=%d", status)
	}

	var req *RequestReadV3
	var offset uint64

	var buf bytes.Buffer

	for {
		req = &RequestReadV3{
			Offset:       offset,
			Handle:       handle,
			RequiredSize: 4096,
		}

		res := new(ReplyReadV3)

		status = c.Dispatch(OpReadV3, req, res).Status
		if status != StatusSuccess {
			t.Fatalf("status=%d", status)
		}

		if Trace {
			fmt.Fprintf(os.Stderr, "read %d: %q\n", res.ActualSize, string(res.Payload))
		}

		offset += uint64(res.ActualSize)
		_, _ = buf.Write(res.Payload)

		if res.ActualSize == 0 {
			break
		}
	}

	status = c.Close(handle)
	if status != StatusSuccess {
		t.Errorf("status=%d", status)
	}

	status = c.DestroySession()
	if status != StatusSuccess {
		t.Errorf("status=%d", status)
	}

	var files []string
	gz, _ := gzip.NewReader(&buf)
	tr := tar.NewReader(gz)

	for {
		header, terr := tr.Next()
		if terr != nil {
			if terr == io.EOF {
				break
			}
			t.Fatal(terr)
		}

		files = append(files, header.Name)

		if header.Typeflag == tar.TypeReg {
			_, err = io.Copy(ioutil.Discard, tr)
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	nfiles++ // symlink
	expect := nfiles*len(dirs) + len(dirs)
	if len(files) != expect {
		t.Errorf("expected %d, files=%d", expect, len(files))
	}

	err = os.RemoveAll(dir)
	if err != nil {
		t.Fatal(err)
	}
}

func TestWriteArchive(t *testing.T) {
	Trace = testing.Verbose()

	dir, err := ioutil.TempDir("", "toolbox-")
	if err != nil {
		t.Fatal(err)
	}

	nfiles := 5

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for i := 0; i < nfiles; i++ {
		data := bytes.NewBufferString(strings.Repeat("X", i+1024))

		header := &tar.Header{
			Name:     fmt.Sprintf("toolbox-file-%d", i),
			ModTime:  time.Now(),
			Mode:     0644,
			Typeflag: tar.TypeReg,
			Size:     int64(data.Len()),
		}

		err = tw.WriteHeader(header)
		if err != nil {
			t.Fatal(err)
		}

		_, _ = tw.Write(data.Bytes())

		if i == 0 {
			err = tw.WriteHeader(&tar.Header{
				Linkname: header.Name,
				Name:     "first-file",
				ModTime:  time.Now(),
				Mode:     0644,
				Typeflag: tar.TypeSymlink,
			})
			if err != nil {
				t.Fatal(err)
			}

			err = tw.WriteHeader(&tar.Header{
				Name:     "subdir",
				ModTime:  time.Now(),
				Mode:     0755,
				Typeflag: tar.TypeDir,
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	_ = tw.Close()
	_ = gz.Close()

	c := NewClient()
	c.s.Archive = true

	status := c.CreateSession()
	if status != StatusSuccess {
		t.Fatalf("status=%d", status)
	}

	_, status = c.GetAttr(dir)
	if status != StatusSuccess {
		t.Errorf("status=%d", status)
	}

	handle, status := c.OpenWrite(dir)
	if status != StatusSuccess {
		t.Fatalf("status=%d", status)
	}

	payload := buf.Bytes()
	size := uint32(buf.Len())

	req := &RequestWriteV3{
		Handle:       handle,
		WriteFlags:   WriteAppend,
		Offset:       0,
		RequiredSize: size,
		Payload:      payload,
	}

	res := new(ReplyReadV3)

	status = c.Dispatch(OpWriteV3, req, res).Status

	if status != StatusSuccess {
		t.Errorf("status=%d", status)
	}

	var attr AttrV2
	info, _ := os.Stat(dir)
	attr.Stat(info)

	status = c.SetAttr(dir, attr)
	if status != StatusSuccess {
		t.Errorf("status=%d", status)
	}

	status = c.Close(handle)
	if status != StatusSuccess {
		t.Errorf("status=%d", status)
	}

	status = c.DestroySession()
	if status != StatusSuccess {
		t.Errorf("status=%d", status)
	}

	files, err := ioutil.ReadDir(dir)
	if err != nil {
		t.Error(err)
	}
	if len(files) != nfiles+2 {
		t.Errorf("files=%d", len(files))
	}

	err = os.RemoveAll(dir)
	if err != nil {
		t.Fatal(err)
	}
}
