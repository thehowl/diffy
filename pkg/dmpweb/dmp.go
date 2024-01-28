// Package dmpweb provides diff/match/patch functions for the web interface of
// diff.am, and some general utility functions we'll be using.
//
// This package is compiled using gopherjs. See the makefile.
package main

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"io"

	"github.com/gopherjs/gopherjs/js"
)

func main() {
	js.Global.Set("dmp", map[string]any{
		"Test":  Test,
		"Files": Files,
	})
}

// File ...
type File struct {
	Name    string
	Content string
}

// Files returns a JSON representation of the files present in a tar archive,
// base64-encoded.
func Files(data string) []File {
	dec := base64.NewDecoder(base64.StdEncoding, bytes.NewReader([]byte(data)))
	rd := tar.NewReader(dec)
	var files []File
	for {
		f, err := rd.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			panic(err.Error())
		}
		if f == nil {
			break
		}
		data, err := io.ReadAll(rd)
		if err != nil {
			panic(err.Error())
		}
		files = append(files, File{Name: f.Name, Content: string(data)})
	}
	return files
}

// Test ...
func Test() int {
	return 1234
}
