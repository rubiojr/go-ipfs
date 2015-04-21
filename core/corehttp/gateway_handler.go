package corehttp

import (
	"fmt"
	"html/template"
	"io"
	"net/http"
	gopath "path"
	"strings"
	"time"

	mh "github.com/ipfs/go-ipfs/Godeps/_workspace/src/github.com/jbenet/go-multihash"
	"github.com/ipfs/go-ipfs/Godeps/_workspace/src/golang.org/x/net/context"

	core "github.com/ipfs/go-ipfs/core"
	"github.com/ipfs/go-ipfs/importer"
	chunk "github.com/ipfs/go-ipfs/importer/chunk"
	dag "github.com/ipfs/go-ipfs/merkledag"
	path "github.com/ipfs/go-ipfs/path"
	"github.com/ipfs/go-ipfs/routing"
	ufs "github.com/ipfs/go-ipfs/unixfs"
	uio "github.com/ipfs/go-ipfs/unixfs/io"
	u "github.com/ipfs/go-ipfs/util"
)

const (
	IpfsPathPrefix = "/ipfs/"
	IpnsPathPrefix = "/ipns/"
)

type gateway interface {
	ResolvePath(string) (*dag.Node, error)
	NewDagFromReader(io.Reader) (*dag.Node, error)
	AddNodeToDAG(nd *dag.Node) (u.Key, error)
	NewDagReader(nd *dag.Node) (uio.ReadSeekCloser, error)
}

// shortcut for templating
type webHandler map[string]interface{}

// struct for directory listing
type directoryItem struct {
	Size uint64
	Name string
	Path string
}

// gatewayHandler is a HTTP handler that serves IPFS objects (accessible by default at /ipfs/<path>)
// (it serves requests like GET /ipfs/QmVRzPKPzNtSrEzBFm2UZfxmPAgnaLke4DMcerbsGGSaFe/link)
type gatewayHandler struct {
	node    *core.IpfsNode
	dirList *template.Template
	config  GatewayConfig
}

func newGatewayHandler(node *core.IpfsNode, conf GatewayConfig) (*gatewayHandler, error) {
	i := &gatewayHandler{
		node:   node,
		config: conf,
	}
	err := i.loadTemplate()
	if err != nil {
		return nil, err
	}
	return i, nil
}

// Load the directroy list template
func (i *gatewayHandler) loadTemplate() error {
	t, err := template.New("dir").Parse(listingTemplate)
	if err != nil {
		return err
	}
	i.dirList = t
	return nil
}

func (i *gatewayHandler) resolveNamePath(ctx context.Context, p string) (string, error) {
	p = gopath.Clean(p)

	if strings.HasPrefix(p, IpnsPathPrefix) {
		elements := strings.Split(p[len(IpnsPathPrefix):], "/")
		hash := elements[0]
		k, err := i.node.Namesys.Resolve(ctx, hash)
		if err != nil {
			return "", err
		}

		elements[0] = k.Pretty()
		p = gopath.Join(elements...)
	}
	if !strings.HasPrefix(p, IpfsPathPrefix) {
		p = gopath.Join(IpfsPathPrefix, p)
	}
	return p, nil
}

func (i *gatewayHandler) ResolvePath(ctx context.Context, p string) (*dag.Node, string, error) {
	p, err := i.resolveNamePath(ctx, p)
	if err != nil {
		return nil, "", err
	}

	node, err := i.node.Resolver.ResolvePath(path.Path(p))
	if err != nil {
		return nil, "", err
	}
	return node, p, err
}

func (i *gatewayHandler) NewDagFromReader(r io.Reader) (*dag.Node, error) {
	return importer.BuildDagFromReader(
		r, i.node.DAG, i.node.Pinning.GetManual(), chunk.DefaultSplitter)
}

func NewDagEmptyDir() *dag.Node {
	return &dag.Node{Data: ufs.FolderPBData()}
}

func (i *gatewayHandler) AddNodeToDAG(nd *dag.Node) (u.Key, error) {
	return i.node.DAG.Add(nd)
}

func (i *gatewayHandler) NewDagReader(nd *dag.Node) (uio.ReadSeekCloser, error) {
	return uio.NewDagReader(i.node.Context(), nd, i.node.DAG)
}

// TODO(btc): break this apart into separate handlers using a more expressive
// muxer
func (i *gatewayHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if i.config.Writable && r.Method == "POST" {
		i.postHandler(w, r)
		return
	}

	if i.config.Writable && r.Method == "PUT" {
		i.putHandler(w, r)
		return
	}

	if i.config.Writable && r.Method == "DELETE" {
		i.deleteHandler(w, r)
		return
	}

	if r.Method == "GET" {
		i.getOrHeadHandler(w, r)
		return
	}

	if r.Method == "HEAD" {
		i.getOrHeadHandler(w, r)
		return
	}

	errmsg := "Method " + r.Method + " not allowed: "
	if !i.config.Writable {
		w.WriteHeader(http.StatusMethodNotAllowed)
		errmsg = errmsg + "read only access"
	} else {
		w.WriteHeader(http.StatusBadRequest)
		errmsg = errmsg + "bad request for " + r.URL.Path
	}
	w.Write([]byte(errmsg))
	log.Debug(errmsg)
}

func (i *gatewayHandler) getOrHeadHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(i.node.Context())
	defer cancel()

	urlPath := r.URL.Path

	if i.config.BlockList != nil && i.config.BlockList.ShouldBlock(urlPath) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("403 - Forbidden"))
		return
	}

	nd, p, err := i.ResolvePath(ctx, urlPath)
	if err != nil {
		webError(w, "Path Resolve error", err, http.StatusBadRequest)
		return
	}

	etag := gopath.Base(p)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("X-IPFS-Path", p)

	// Suborigin header, sandboxes apps from each other in the browser (even
	// though they are served from the same gateway domain). NOTE: This is not
	// yet widely supported by browsers.
	pathRoot := strings.SplitN(urlPath, "/", 4)[2]
	w.Header().Set("Suborigin", pathRoot)

	dr, err := i.NewDagReader(nd)
	if err != nil && err != uio.ErrIsDir {
		// not a directory and still an error
		internalWebError(w, err)
		return
	}

	// set these headers _after_ the error, for we may just not have it
	// and dont want the client to cache a 500 response...
	// and only if it's /ipfs!
	// TODO: break this out when we split /ipfs /ipns routes.
	modtime := time.Now()
	if strings.HasPrefix(urlPath, IpfsPathPrefix) {
		w.Header().Set("Etag", etag)
		w.Header().Set("Cache-Control", "public, max-age=29030400")

		// set modtime to a really long time ago, since files are immutable and should stay cached
		modtime = time.Unix(1, 0)
	}

	if err == nil {
		defer dr.Close()
		_, name := gopath.Split(urlPath)
		http.ServeContent(w, r, name, modtime, dr)
		return
	}

	// storage for directory listing
	var dirListing []directoryItem
	// loop through files
	foundIndex := false
	for _, link := range nd.Links {
		if link.Name == "index.html" {
			if urlPath[len(urlPath)-1] != '/' {
				http.Redirect(w, r, urlPath+"/", 302)
				return
			}

			log.Debug("found index")
			foundIndex = true
			// return index page instead.
			nd, _, err := i.ResolvePath(ctx, urlPath+"/index.html")
			if err != nil {
				internalWebError(w, err)
				return
			}
			dr, err := i.NewDagReader(nd)
			if err != nil {
				internalWebError(w, err)
				return
			}
			defer dr.Close()

			// write to request
			if r.Method != "HEAD" {
				io.Copy(w, dr)
			}
			break
		}

		di := directoryItem{link.Size, link.Name, gopath.Join(urlPath, link.Name)}
		dirListing = append(dirListing, di)
	}

	if !foundIndex {
		// template and return directory listing
		hndlr := webHandler{
			"listing": dirListing,
			"path":    urlPath,
		}

		if r.Method != "HEAD" {
			if err := i.dirList.Execute(w, hndlr); err != nil {
				internalWebError(w, err)
				return
			}
		}
	}
}

func (i *gatewayHandler) postHandler(w http.ResponseWriter, r *http.Request) {
	nd, err := i.NewDagFromReader(r.Body)
	if err != nil {
		internalWebError(w, err)
		return
	}

	k, err := i.AddNodeToDAG(nd)
	if err != nil {
		internalWebError(w, err)
		return
	}

	h := mh.Multihash(k).B58String()
	w.Header().Set("IPFS-Hash", h)
	http.Redirect(w, r, IpfsPathPrefix+h, http.StatusCreated)
}

func (i *gatewayHandler) putEmptyDirHandler(w http.ResponseWriter, r *http.Request) {
	newnode := NewDagEmptyDir()

	key, err := i.node.DAG.Add(newnode)
	if err != nil {
		webError(w, "Could not recursively add new node", err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("IPFS-Hash", key.String())
	http.Redirect(w, r, IpfsPathPrefix+key.String()+"/", http.StatusCreated)
}

func (i *gatewayHandler) putHandler(w http.ResponseWriter, r *http.Request) {
	urlPath := r.URL.Path
	pathext := urlPath[5:]
	var err error
	if urlPath == IpfsPathPrefix+"QmUNLLsPACCz1vLxQVkXqqLX5R1X345qqfHbsf67hvA3Nn/" {
		i.putEmptyDirHandler(w, r)
		return
	}

	var newnode *dag.Node
	if pathext[len(pathext)-1] == '/' {
		newnode = NewDagEmptyDir()
	} else {
		newnode, err = i.NewDagFromReader(r.Body)
		if err != nil {
			webError(w, "Could not create DAG from request", err, http.StatusInternalServerError)
			return
		}
	}

	ctx, cancel := context.WithCancel(i.node.Context())
	defer cancel()

	ipfspath, err := i.resolveNamePath(ctx, urlPath)
	if err != nil {
		// FIXME HTTP error code
		webError(w, "Could not resolve name", err, http.StatusInternalServerError)
		return
	}

	h, components, err := path.SplitAbsPath(path.Path(ipfspath))
	if err != nil {
		webError(w, "Could not split path", err, http.StatusInternalServerError)
		return
	}

	if len(components) < 1 {
		err = fmt.Errorf("Cannot override existing object")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		log.Debug("%s", err)
		return
	}

	tctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	rootnd, err := i.node.Resolver.DAG.Get(tctx, u.Key(h))
	if err != nil {
		webError(w, "Could not resolve root object", err, http.StatusBadRequest)
		return
	}

	// resolving path components into merkledag nodes. if a component does not
	// resolve, create empty directories (which will be linked and populated below.)
	path_nodes, err := i.node.Resolver.ResolveLinks(rootnd, components[:len(components)-1])
	if _, ok := err.(path.ErrNoLink); ok {
		// Create empty directories, links will be made further down the code
		for len(path_nodes) < len(components) {
			path_nodes = append(path_nodes, NewDagEmptyDir())
		}
	} else if err != nil {
		webError(w, "Could not resolve parent object", err, http.StatusBadRequest)
		return
	}

	for i := len(path_nodes) - 1; i >= 0; i-- {
		newnode, err = path_nodes[i].UpdateNodeLink(components[i], newnode)
		if err != nil {
			webError(w, "Could not update node links", err, http.StatusInternalServerError)
			return
		}
	}

	err = i.node.DAG.AddRecursive(newnode)
	if err != nil {
		webError(w, "Could not add recursively new node", err, http.StatusInternalServerError)
		return
	}

	// Redirect to new path
	key, err := newnode.Key()
	if err != nil {
		webError(w, "Could not get key of new node", err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("IPFS-Hash", key.String())
	http.Redirect(w, r, IpfsPathPrefix+key.String()+"/"+strings.Join(components, "/"), http.StatusCreated)
}

func (i *gatewayHandler) deleteHandler(w http.ResponseWriter, r *http.Request) {
	urlPath := r.URL.Path
	ctx, cancel := context.WithCancel(i.node.Context())
	defer cancel()

	ipfspath, err := i.resolveNamePath(ctx, urlPath)
	if err != nil {
		// FIXME HTTP error code
		webError(w, "Could not resolve name", err, http.StatusInternalServerError)
		return
	}

	h, components, err := path.SplitAbsPath(path.Path(ipfspath))
	if err != nil {
		webError(w, "Could not split path", err, http.StatusInternalServerError)
		return
	}

	tctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	rootnd, err := i.node.Resolver.DAG.Get(tctx, u.Key(h))
	if err != nil {
		webError(w, "Could not resolve root object", err, http.StatusBadRequest)
		return
	}

	path_nodes, err := i.node.Resolver.ResolveLinks(rootnd, components[:len(components)-1])
	if err != nil {
		webError(w, "Could not resolve parent object", err, http.StatusBadRequest)
		return
	}

	err = path_nodes[len(path_nodes)-1].RemoveNodeLink(components[len(components)-1])
	if err != nil {
		webError(w, "Could not delete link", err, http.StatusBadRequest)
		return
	}

	newnode := path_nodes[len(path_nodes)-1]
	for i := len(path_nodes) - 2; i >= 0; i-- {
		newnode, err = path_nodes[i].UpdateNodeLink(components[i], newnode)
		if err != nil {
			webError(w, "Could not update node links", err, http.StatusInternalServerError)
			return
		}
	}

	err = i.node.DAG.AddRecursive(newnode)
	if err != nil {
		webError(w, "Could not add recursively new node", err, http.StatusInternalServerError)
		return
	}

	// Redirect to new path
	key, err := newnode.Key()
	if err != nil {
		webError(w, "Could not get key of new node", err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("IPFS-Hash", key.String())
	http.Redirect(w, r, IpfsPathPrefix+key.String()+"/"+strings.Join(components[:len(components)-1], "/"), http.StatusCreated)
}

func webError(w http.ResponseWriter, message string, err error, defaultCode int) {
	if _, ok := err.(path.ErrNoLink); ok {
		webErrorWithCode(w, message, err, http.StatusNotFound)
	} else if err == routing.ErrNotFound {
		webErrorWithCode(w, message, err, http.StatusNotFound)
	} else if err == context.DeadlineExceeded {
		webErrorWithCode(w, message, err, http.StatusRequestTimeout)
	} else {
		webErrorWithCode(w, message, err, defaultCode)
	}
}

func webErrorWithCode(w http.ResponseWriter, message string, err error, code int) {
	w.WriteHeader(code)
	log.Debugf("%s: %s", message, err)
	w.Write([]byte(message + ": " + err.Error()))
}

// return a 500 error and log
func internalWebError(w http.ResponseWriter, err error) {
	w.WriteHeader(http.StatusInternalServerError)
	w.Write([]byte(err.Error()))
	log.Debug("%s", err)
}

// Directory listing template
var listingTemplate = `
<!DOCTYPE html>
<html>
	<head>
		<meta charset="utf-8" />
		<title>{{ .path }}</title>
	</head>
	<body>
	<h2>Index of {{ .path }}</h2>
	<ul>
	<li><a href="./..">..</a></li>
  {{ range .listing }}
	<li><a href="{{ .Path }}">{{ .Name }}</a> - {{ .Size }} bytes</li>
	{{ end }}
	</ul>
	</body>
</html>
`
