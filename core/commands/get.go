package commands

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	gopath "path"
	"strings"

	cmds "github.com/ipfs/go-ipfs/commands"
	core "github.com/ipfs/go-ipfs/core"
	path "github.com/ipfs/go-ipfs/path"
	tar "github.com/ipfs/go-ipfs/thirdparty/tar"
	utar "github.com/ipfs/go-ipfs/unixfs/tar"

	"github.com/ipfs/go-ipfs/Godeps/_workspace/src/github.com/cheggaaa/pb"
)

var ErrInvalidCompressionLevel = errors.New("Compression level must be between 1 and 9")

var GetCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline: "Download IPFS objects",
		ShortDescription: `
Retrieves the object named by <ipfs-or-ipns-path> and stores the data to disk.

By default, the output will be stored at ./<ipfs-path>, but an alternate path
can be specified with '--output=<path>' or '-o=<path>'.

To output a TAR archive instead of unpacked files, use '--archive' or '-a'.

To compress the output with GZIP compression, use '--compress' or '-C'. You
may also specify the level of compression by specifying '-l=<1-9>'.
`,
	},

	Arguments: []cmds.Argument{
		cmds.StringArg("ipfs-path", true, false, "The path to the IPFS object(s) to be outputted").EnableStdin(),
	},
	Options: []cmds.Option{
		cmds.StringOption("output", "o", "The path where output should be stored"),
		cmds.BoolOption("archive", "a", "Output a TAR archive"),
		cmds.BoolOption("compress", "C", "Compress the output with GZIP compression"),
		cmds.IntOption("compression-level", "l", "The level of compression (1-9)"),
	},
	PreRun: func(req cmds.Request) error {
		_, err := getCompressOptions(req)
		return err
	},
	Run: func(req cmds.Request, res cmds.Response) {
		cmplvl, err := getCompressOptions(req)
		if err != nil {
			res.SetError(err, cmds.ErrClient)
			return
		}

		node, err := req.Context().GetNode()
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		reader, err := get(node, req.Arguments()[0], cmplvl)
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}
		res.SetOutput(reader)
	},
	PostRun: func(req cmds.Request, res cmds.Response) {
		if res.Output() == nil {
			return
		}
		outReader := res.Output().(io.Reader)
		res.SetOutput(nil)

		outPath, _, _ := req.Option("output").String()
		if len(outPath) == 0 {
			_, outPath = gopath.Split(req.Arguments()[0])
			outPath = gopath.Clean(outPath)
		}

		cmplvl, err := getCompressOptions(req)
		if err != nil {
			res.SetError(err, cmds.ErrClient)
			return
		}

		if archive, _, _ := req.Option("archive").Bool(); archive {
			if !strings.HasSuffix(outPath, ".tar") {
				outPath += ".tar"
			}
			if cmplvl != gzip.NoCompression {
				outPath += ".gz"
			}
			fmt.Printf("Saving archive to %s\n", outPath)

			file, err := os.Create(outPath)
			if err != nil {
				res.SetError(err, cmds.ErrNormal)
				return
			}
			defer file.Close()

			bar := pb.New(0).SetUnits(pb.U_BYTES)
			bar.Output = os.Stderr
			pbReader := bar.NewProxyReader(outReader)
			bar.Start()
			defer bar.Finish()

			_, err = io.Copy(file, pbReader)
			if err != nil {
				res.SetError(err, cmds.ErrNormal)
				return
			}

			return
		}

		fmt.Printf("Saving file(s) to %s\n", outPath)

		// TODO: get total length of files
		bar := pb.New(0).SetUnits(pb.U_BYTES)
		bar.Output = os.Stderr

		// wrap the reader with the progress bar proxy reader
		// if the output is compressed, also wrap it in a gzip.Reader
		var reader io.Reader
		if cmplvl != gzip.NoCompression {
			gzipReader, err := gzip.NewReader(outReader)
			if err != nil {
				res.SetError(err, cmds.ErrNormal)
				return
			}
			defer gzipReader.Close()
			reader = bar.NewProxyReader(gzipReader)
		} else {
			reader = bar.NewProxyReader(outReader)
		}

		bar.Start()
		defer bar.Finish()

		extractor := &tar.Extractor{outPath}
		err = extractor.Extract(reader)
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
		}
	},
}

func getCompressOptions(req cmds.Request) (int, error) {
	cmprs, _, _ := req.Option("compress").Bool()
	cmplvl, cmplvlFound, _ := req.Option("compression-level").Int()
	switch {
	case !cmprs:
		return gzip.NoCompression, nil
	case cmprs && !cmplvlFound:
		return gzip.DefaultCompression, nil
	case cmprs && cmplvlFound && (cmplvl < 1 || cmplvl > 9):
		return gzip.NoCompression, ErrInvalidCompressionLevel
	}
	return gzip.NoCompression, nil
}

func get(node *core.IpfsNode, p string, compression int) (io.Reader, error) {
	pathToResolve := path.Path(p)
	dagnode, err := core.Resolve(node, pathToResolve)
	if err != nil {
		return nil, err
	}

	return utar.NewReader(pathToResolve, node.DAG, dagnode, compression)
}
