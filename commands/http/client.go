package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	cmds "github.com/ipfs/go-ipfs/commands"
	config "github.com/ipfs/go-ipfs/repo/config"
)

const (
	ApiUrlFormat = "http://%s%s/%s?%s"
	ApiPath      = "/api/v0" // TODO: make configurable
)

// Client is the commands HTTP client interface.
type Client interface {
	Send(req cmds.Request) (cmds.Response, error)
}

type client struct {
	serverAddress string
}

func NewClient(address string) Client {
	return &client{address}
}

func (c *client) Send(req cmds.Request) (cmds.Response, error) {

	// save user-provided encoding
	previousUserProvidedEncoding, found, err := req.Option(cmds.EncShort).String()
	if err != nil {
		return nil, err
	}

	// override with json to send to server
	req.SetOption(cmds.EncShort, cmds.JSON)

	// stream channel output
	req.SetOption(cmds.ChanOpt, "true")

	query, err := getQuery(req)
	if err != nil {
		return nil, err
	}

	var fileReader *MultiFileReader
	var reader io.Reader

	if req.Files() != nil {
		fileReader = NewMultiFileReader(req.Files(), true)
		reader = fileReader
	} else {
		// if we have no file data, use an empty Reader
		// (http.NewRequest panics when a nil Reader is used)
		reader = strings.NewReader("")
	}

	path := strings.Join(req.Path(), "/")
	url := fmt.Sprintf(ApiUrlFormat, c.serverAddress, ApiPath, path, query)

	httpReq, err := http.NewRequest("POST", url, reader)
	if err != nil {
		return nil, err
	}

	// TODO extract string consts?
	if fileReader != nil {
		httpReq.Header.Set("Content-Type", "multipart/form-data; boundary="+fileReader.Boundary())
		httpReq.Header.Set("Content-Disposition", "form-data: name=\"files\"")
	} else {
		httpReq.Header.Set("Content-Type", "application/octet-stream")
	}
	version := config.CurrentVersionNumber
	httpReq.Header.Set("User-Agent", fmt.Sprintf("/go-ipfs/%s/", version))

	ec := make(chan error, 1)
	rc := make(chan cmds.Response, 1)
	dc := req.Context().Context.Done()

	go func() {
		httpRes, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			ec <- err
			return
		}
		// using the overridden JSON encoding in request
		res, err := getResponse(httpRes, req)
		if err != nil {
			ec <- err
			return
		}
		rc <- res
	}()

	for {
		select {
		case <-dc:
			log.Debug("Context cancelled, cancelling HTTP request...")
			tr := http.DefaultTransport.(*http.Transport)
			tr.CancelRequest(httpReq)
			dc = nil // Wait for ec or rc
		case err := <-ec:
			return nil, err
		case res := <-rc:
			if found && len(previousUserProvidedEncoding) > 0 {
				// reset to user provided encoding after sending request
				// NB: if user has provided an encoding but it is the empty string,
				// still leave it as JSON.
				req.SetOption(cmds.EncShort, previousUserProvidedEncoding)
			}
			return res, nil
		}
	}
}

func getQuery(req cmds.Request) (string, error) {
	query := url.Values{}
	for k, v := range req.Options() {
		str := fmt.Sprintf("%v", v)
		query.Set(k, str)
	}

	args := req.Arguments()
	argDefs := req.Command().Arguments

	argDefIndex := 0

	for _, arg := range args {
		argDef := argDefs[argDefIndex]
		// skip ArgFiles
		for argDef.Type == cmds.ArgFile {
			argDefIndex++
			argDef = argDefs[argDefIndex]
		}

		query.Add("arg", arg)

		if len(argDefs) > argDefIndex+1 {
			argDefIndex++
		}
	}

	return query.Encode(), nil
}

// getResponse decodes a http.Response to create a cmds.Response
func getResponse(httpRes *http.Response, req cmds.Request) (cmds.Response, error) {
	var err error
	res := cmds.NewResponse(req)

	contentType := httpRes.Header.Get(contentTypeHeader)
	contentType = strings.Split(contentType, ";")[0]

	lengthHeader := httpRes.Header.Get(contentLengthHeader)
	if len(lengthHeader) > 0 {
		length, err := strconv.ParseUint(lengthHeader, 10, 64)
		if err != nil {
			return nil, err
		}
		res.SetLength(length)
	}

	if len(httpRes.Header.Get(streamHeader)) > 0 {
		// if output is a stream, we can just use the body reader
		res.SetOutput(httpRes.Body)
		return res, nil

	} else if len(httpRes.Header.Get(channelHeader)) > 0 {
		// if output is coming from a channel, decode each chunk
		outChan := make(chan interface{})
		go func() {
			dec := json.NewDecoder(httpRes.Body)
			outputType := reflect.TypeOf(req.Command().Type)

			ctx := req.Context().Context

			for {
				var v interface{}
				var err error
				if outputType != nil {
					v = reflect.New(outputType).Interface()
					err = dec.Decode(v)
				} else {
					err = dec.Decode(&v)
				}
				if err != nil && err != io.EOF {
					fmt.Println(err.Error())
					return
				}

				select {
				case <-ctx.Done():
					close(outChan)
					return
				default:
				}

				if err == io.EOF {
					close(outChan)
					return
				}
				outChan <- v
			}
		}()

		res.SetOutput((<-chan interface{})(outChan))
		return res, nil
	}

	dec := json.NewDecoder(httpRes.Body)

	if httpRes.StatusCode >= http.StatusBadRequest {
		e := cmds.Error{}

		if httpRes.StatusCode == http.StatusNotFound {
			// handle 404s
			e.Message = "Command not found."
			e.Code = cmds.ErrClient

		} else if contentType == "text/plain" {
			// handle non-marshalled errors
			buf := bytes.NewBuffer(nil)
			io.Copy(buf, httpRes.Body)
			e.Message = string(buf.Bytes())
			e.Code = cmds.ErrNormal

		} else {
			// handle marshalled errors
			err = dec.Decode(&e)
			if err != nil {
				return nil, err
			}
		}

		res.SetError(e, e.Code)

	} else {
		outputType := reflect.TypeOf(req.Command().Type)
		var v interface{}

		if outputType != nil {
			v = reflect.New(outputType).Interface()
			err = dec.Decode(v)
		} else {
			err = dec.Decode(&v)
		}
		if err != nil && err != io.EOF {
			return nil, err
		}
		if v != nil {
			res.SetOutput(v)
		}
	}

	return res, nil
}
