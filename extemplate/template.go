// Copyright 2017 Danny van Kooten. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.
// modified to work in addition with packr

package extemplate

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/orange-cloudfoundry/statusetat/common"
	"github.com/orange-cloudfoundry/statusetat/markdown"
	"github.com/orange-cloudfoundry/statusetat/models"
)

var extendsRegex *regexp.Regexp

var registeredFuncs = template.FuncMap{
	"iconState":             iconState,
	"colorState":            colorState,
	"colorIncidentState":    colorIncidentState,
	"textIncidentState":     models.TextIncidentState,
	"textScheduledState":    models.TextScheduledState,
	"textState":             models.TextState,
	"timeFormat":            timeFormat,
	"timeStdFormat":         timeStdFormat,
	"title":                 common.Title,
	"markdown":              markdown.ConvertSafeTemplate,
	"stateFromIncidents":    stateFromIncidents,
	"safeHTML":              safeHTML,
	"humanTime":             humanTime,
	"jsonify":               jsonify,
	"listMap":               listMap,
	"humanDuration":         common.HumanDuration,
	"isAfterNow":            isAfterNow,
	"markdownNoParaph":      markdownNoParaph,
	"tagify":                tagify,
	"ref":                   ref,
	"timeFmtCustom":         timeFmtCustom,
	"colorHexState":         colorHexState,
	"colorHexIncidentState": colorHexIncidentState,
	"join":                  strings.Join,
	"netUrl":                netUrl,
	"timeNow":               timeNow,
	"dict":                  dict,
	"metadataValue":         metadataValue,
	"timeAddDay":            timeAddDay,
	"stringReplace":         stringReplace,
	"sanitizeUrl":           sanitizeUrl,
}

func AddFuncs(funcs template.FuncMap) {
	for k, v := range funcs {
		registeredFuncs[k] = v
	}
}

func Funcs() template.FuncMap {
	return registeredFuncs
}

// Extemplate holds a reference to all templates
// and shared configuration like Delims or FuncMap
type Extemplate struct {
	shared    *template.Template
	templates map[string]*template.Template
}

type templatefile struct {
	contents []byte
	layout   string
}

func init() {
	var err error
	extendsRegex, err = regexp.Compile(`\{\{ *?extends +?"(.+?)" *?\}\}`)
	if err != nil {
		panic(err)
	}
}

// New allocates a new, empty, template map
func New() *Extemplate {
	shared := template.New("").Funcs(registeredFuncs)
	return &Extemplate{
		shared:    shared,
		templates: make(map[string]*template.Template),
	}
}

// Delims sets the action delimiters to the specified strings,
// to be used in subsequent calls to ParseDir.
// Nested template  definitions will inherit the settings.
// An empty delimiter stands for the corresponding default: {{ or }}.
// The return value is the template, so calls can be chained.
func (x *Extemplate) Delims(left, right string) *Extemplate {
	x.shared.Delims(left, right)
	return x
}

// Funcs adds the elements of the argument map to the template's function map.
// It must be called before templates are parsed
// It panics if a value in the map is not a function with appropriate return
// type or if the name cannot be used syntactically as a function in a template.
// It is legal to overwrite elements of the map. The return value is the Extemplate instance,
// so calls can be chained.
func (x *Extemplate) Funcs(funcMap template.FuncMap) *Extemplate {
	x.shared.Funcs(funcMap)
	return x
}

// Lookup returns the template with the given name
// It returns nil if there is no such template or the template has no definition.
func (x *Extemplate) Lookup(name string) *template.Template {
	if t, ok := x.templates[name]; ok {
		return t
	}

	return nil
}

// ExecuteTemplate applies the template named name to the specified data object and writes the output to wr.
func (x *Extemplate) ExecuteTemplate(wr io.Writer, name string, data interface{}) error {
	tmpl := x.Lookup(name)
	if tmpl == nil {
		return fmt.Errorf("extemplate: no template %q", name)
	}

	return tmpl.Execute(wr, data)
}

// var assets embed.FS

// func Assests() (fs.FS, error) {
// 	return fs.Sub(assets, "templates")
// }

// assets, _ := ui.Assests()

// ParseDir walks the given directory root and parses all files with any of the registered extensions.
// Default extensions are .html and .tmpl
// If a template file has {{/* extends "other-file.tmpl" */}} as its first line it will parse that file for base templates.
// Parsed templates are named relative to the given root directory
func (x *Extemplate) ParseDir(fs *embed.FS, path string, extensions []string) error {
	var b []byte
	var err error

	files, err := findTemplateFiles(fs, path, extensions)
	if err != nil {
		return err
	}

	// parse all non-child templates into the shared template namespace
	for name, tf := range files {
		if tf.layout != "" {
			continue
		}

		_, err = x.shared.New(name).Parse(string(tf.contents))
		if err != nil {
			return err
		}
	}

	// then, parse all templates again but with inheritance
	for name, tf := range files {

		// if this is a non-child template, no need to re-parse
		if tf.layout == "" {
			x.templates[name] = x.shared.Lookup(name)
			continue
		}

		tmpl := template.Must(x.shared.Clone()).New(name)

		// add to set under normalized name (path from root)
		x.templates[name] = tmpl

		// parse parent templates
		templateFiles := []string{name}
		pname := tf.layout
		parent, parentExists := files[pname]
		for parentExists {
			templateFiles = append(templateFiles, pname)
			pname = parent.layout
			parent, parentExists = files[parent.layout]
		}

		// parse template files in reverse order (because children should override parents)
		for j := len(templateFiles) - 1; j >= 0; j-- {
			b = files[templateFiles[j]].contents
			_, err = tmpl.Parse(string(b))
			if err != nil {
				return err
			}
		}

	}

	return nil
}

func findTemplateFiles(efs *embed.FS, path string, extensions []string) (map[string]*templatefile, error) {
	var files = map[string]*templatefile{}
	var exts = map[string]bool{}

	// create map of allowed extensions
	for _, e := range extensions {
		exts[e] = true
	}

	// find all template files
	err := fs.WalkDir(efs, path, func(templatepath string, d fs.DirEntry, err error) error {
		if !d.IsDir() {
			// skip if extension not in list of allowed extensions
			e := filepath.Ext(d.Name())
			if _, ok := exts[e]; !ok {
				return nil
			}
			// read file into memory
			contents, err := efs.ReadFile(templatepath)
			if err != nil {
				return err
			}
			// parse file into templatefile
			tf, err := newTemplateFile(contents)
			if err != nil {
				return err
			}
			relativename, err := filepath.Rel(path, templatepath)
			if err != nil {
				return err
			}
			files[relativename] = tf
			return nil
		}
		return nil
	})
	return files, err
}

// newTemplateFile parses the file contents into something that text/template can understand
func newTemplateFile(c []byte) (*templatefile, error) {
	tf := &templatefile{
		contents: c,
	}

	r := bytes.NewReader(tf.contents)
	pos := 0
	var line []byte
	for {
		ch, l, err := r.ReadRune()
		pos += l

		// read until first line or EOF
		if ch == '\n' || err == io.EOF {
			line = c[0:pos]
			break
		}
	}

	if len(line) < 10 {
		return tf, nil
	}

	// if we have a match, strip first line of content
	if m := extendsRegex.FindSubmatch(line); m != nil {
		tf.layout = string(m[1])
		tf.contents = c[len(line):]
	}

	return tf, nil
}
