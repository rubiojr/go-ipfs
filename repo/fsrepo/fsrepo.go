package fsrepo

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"

	ds "github.com/ipfs/go-ipfs/Godeps/_workspace/src/github.com/jbenet/go-datastore"
	"github.com/ipfs/go-ipfs/Godeps/_workspace/src/github.com/jbenet/go-datastore/flatfs"
	levelds "github.com/ipfs/go-ipfs/Godeps/_workspace/src/github.com/jbenet/go-datastore/leveldb"
	"github.com/ipfs/go-ipfs/Godeps/_workspace/src/github.com/jbenet/go-datastore/mount"
	ldbopts "github.com/ipfs/go-ipfs/Godeps/_workspace/src/github.com/syndtr/goleveldb/leveldb/opt"
	repo "github.com/ipfs/go-ipfs/repo"
	"github.com/ipfs/go-ipfs/repo/common"
	config "github.com/ipfs/go-ipfs/repo/config"
	lockfile "github.com/ipfs/go-ipfs/repo/fsrepo/lock"
	mfsr "github.com/ipfs/go-ipfs/repo/fsrepo/migrations"
	serialize "github.com/ipfs/go-ipfs/repo/fsrepo/serialize"
	dir "github.com/ipfs/go-ipfs/thirdparty/dir"
	"github.com/ipfs/go-ipfs/thirdparty/eventlog"
	u "github.com/ipfs/go-ipfs/util"
	util "github.com/ipfs/go-ipfs/util"
	ds2 "github.com/ipfs/go-ipfs/util/datastore2"
)

// version number that we are currently expecting to see
var RepoVersion = "2"

var migrationInstructions = `See https://github.com/ipfs/fs-repo-migrations/blob/master/run.md
Sorry for the inconvenience. In the future, these will run automatically.`

var errIncorrectRepoFmt = `Repo has incorrect version: %s
Program version is: %s
Please run the ipfs migration tool before continuing.
` + migrationInstructions

var (
	ErrNoRepo    = errors.New("no ipfs repo found. please run: ipfs init")
	ErrNoVersion = errors.New("no version file found, please run 0-to-1 migration tool.\n" + migrationInstructions)
	ErrOldRepo   = errors.New("ipfs repo found in old '~/.go-ipfs' location, please run migration tool.\n" + migrationInstructions)
)

const (
	leveldbDirectory = "datastore"
	flatfsDirectory  = "blocks"
)

var (

	// packageLock must be held to while performing any operation that modifies an
	// FSRepo's state field. This includes Init, Open, Close, and Remove.
	packageLock sync.Mutex

	// onlyOne keeps track of open FSRepo instances.
	//
	// TODO: once command Context / Repo integration is cleaned up,
	// this can be removed. Right now, this makes ConfigCmd.Run
	// function try to open the repo twice:
	//
	//     $ ipfs daemon &
	//     $ ipfs config foo
	//
	// The reason for the above is that in standalone mode without the
	// daemon, `ipfs config` tries to save work by not building the
	// full IpfsNode, but accessing the Repo directly.
	onlyOne repo.OnlyOne
)

// FSRepo represents an IPFS FileSystem Repo. It is safe for use by multiple
// callers.
type FSRepo struct {
	// has Close been called already
	closed bool
	// path is the file-system path
	path string
	// lockfile is the file system lock to prevent others from opening
	// the same fsrepo path concurrently
	lockfile io.Closer
	config   *config.Config
	ds       ds.ThreadSafeDatastore
	// tracked separately for use in Close; do not use directly.
	leveldbDS levelds.Datastore
}

var _ repo.Repo = (*FSRepo)(nil)

// Open the FSRepo at path. Returns an error if the repo is not
// initialized.
func Open(repoPath string) (repo.Repo, error) {
	fn := func() (repo.Repo, error) {
		return open(repoPath)
	}
	return onlyOne.Open(repoPath, fn)
}

func open(repoPath string) (repo.Repo, error) {
	packageLock.Lock()
	defer packageLock.Unlock()

	r, err := newFSRepo(repoPath)
	if err != nil {
		return nil, err
	}

	// Check if its initialized
	if err := checkInitialized(r.path); err != nil {
		return nil, err
	}

	r.lockfile, err = lockfile.Lock(r.path)
	if err != nil {
		return nil, err
	}
	keepLocked := false
	defer func() {
		// unlock on error, leave it locked on success
		if !keepLocked {
			r.lockfile.Close()
		}
	}()

	// Check version, and error out if not matching
	ver, err := mfsr.RepoPath(r.path).Version()
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoVersion
		}
		return nil, err
	}

	if ver != RepoVersion {
		return nil, fmt.Errorf(errIncorrectRepoFmt, ver, RepoVersion)
	}

	// check repo path, then check all constituent parts.
	if err := dir.Writable(r.path); err != nil {
		return nil, err
	}

	if err := r.openConfig(); err != nil {
		return nil, err
	}

	if err := r.openDatastore(); err != nil {
		return nil, err
	}

	// setup eventlogger
	configureEventLoggerAtRepoPath(r.config, r.path)

	keepLocked = true
	return r, nil
}

func newFSRepo(rpath string) (*FSRepo, error) {
	expPath, err := u.TildeExpansion(path.Clean(rpath))
	if err != nil {
		return nil, err
	}

	return &FSRepo{path: expPath}, nil
}

func checkInitialized(path string) error {
	if !isInitializedUnsynced(path) {
		alt := strings.Replace(path, ".ipfs", ".go-ipfs", 1)
		if isInitializedUnsynced(alt) {
			return ErrOldRepo
		}
		return ErrNoRepo
	}
	return nil
}

// ConfigAt returns an error if the FSRepo at the given path is not
// initialized. This function allows callers to read the config file even when
// another process is running and holding the lock.
func ConfigAt(repoPath string) (*config.Config, error) {

	// packageLock must be held to ensure that the Read is atomic.
	packageLock.Lock()
	defer packageLock.Unlock()

	configFilename, err := config.Filename(repoPath)
	if err != nil {
		return nil, err
	}
	return serialize.Load(configFilename)
}

// configIsInitialized returns true if the repo is initialized at
// provided |path|.
func configIsInitialized(path string) bool {
	configFilename, err := config.Filename(path)
	if err != nil {
		return false
	}
	if !util.FileExists(configFilename) {
		return false
	}
	return true
}

func initConfig(path string, conf *config.Config) error {
	if configIsInitialized(path) {
		return nil
	}
	configFilename, err := config.Filename(path)
	if err != nil {
		return err
	}
	// initialization is the one time when it's okay to write to the config
	// without reading the config from disk and merging any user-provided keys
	// that may exist.
	if err := serialize.WriteConfigFile(configFilename, conf); err != nil {
		return err
	}
	return nil
}

// Init initializes a new FSRepo at the given path with the provided config.
// TODO add support for custom datastores.
func Init(repoPath string, conf *config.Config) error {

	// packageLock must be held to ensure that the repo is not initialized more
	// than once.
	packageLock.Lock()
	defer packageLock.Unlock()

	if isInitializedUnsynced(repoPath) {
		return nil
	}

	if err := initConfig(repoPath, conf); err != nil {
		return err
	}

	// The actual datastore contents are initialized lazily when Opened.
	// During Init, we merely check that the directory is writeable.
	leveldbPath := path.Join(repoPath, leveldbDirectory)
	if err := dir.Writable(leveldbPath); err != nil {
		return fmt.Errorf("datastore: %s", err)
	}

	flatfsPath := path.Join(repoPath, flatfsDirectory)
	if err := dir.Writable(flatfsPath); err != nil {
		return fmt.Errorf("datastore: %s", err)
	}

	if err := dir.Writable(path.Join(repoPath, "logs")); err != nil {
		return err
	}

	if err := mfsr.RepoPath(repoPath).WriteVersion(RepoVersion); err != nil {
		return err
	}

	return nil
}

// Remove recursively removes the FSRepo at |path|.
func Remove(repoPath string) error {
	repoPath = path.Clean(repoPath)
	return os.RemoveAll(repoPath)
}

// LockedByOtherProcess returns true if the FSRepo is locked by another
// process. If true, then the repo cannot be opened by this process.
func LockedByOtherProcess(repoPath string) bool {
	repoPath = path.Clean(repoPath)

	// TODO replace this with the "api" file
	// https://github.com/ipfs/specs/tree/master/repo/fs-repo

	// NB: the lock is only held when repos are Open
	return lockfile.Locked(repoPath)
}

// openConfig returns an error if the config file is not present.
func (r *FSRepo) openConfig() error {
	configFilename, err := config.Filename(r.path)
	if err != nil {
		return err
	}
	conf, err := serialize.Load(configFilename)
	if err != nil {
		return err
	}
	r.config = conf
	return nil
}

// openDatastore returns an error if the config file is not present.
func (r *FSRepo) openDatastore() error {
	leveldbPath := path.Join(r.path, leveldbDirectory)
	var err error
	// save leveldb reference so it can be neatly closed afterward
	r.leveldbDS, err = levelds.NewDatastore(leveldbPath, &levelds.Options{
		Compression: ldbopts.NoCompression,
	})
	if err != nil {
		return errors.New("unable to open leveldb datastore")
	}

	// 4TB of 256kB objects ~=17M objects, splitting that 256-way
	// leads to ~66k objects per dir, splitting 256*256-way leads to
	// only 256.
	//
	// The keys seen by the block store have predictable prefixes,
	// including "/" from datastore.Key and 2 bytes from multihash. To
	// reach a uniform 256-way split, we need approximately 4 bytes of
	// prefix.
	blocksDS, err := flatfs.New(path.Join(r.path, flatfsDirectory), 4)
	if err != nil {
		return errors.New("unable to open flatfs datastore")
	}

	mountDS := mount.New([]mount.Mount{
		{Prefix: ds.NewKey("/blocks"), Datastore: blocksDS},
		{Prefix: ds.NewKey("/"), Datastore: r.leveldbDS},
	})
	// Make sure it's ok to claim the virtual datastore from mount as
	// threadsafe. There's no clean way to make mount itself provide
	// this information without copy-pasting the code into two
	// variants. This is the same dilemma as the `[].byte` attempt at
	// introducing const types to Go.
	var _ ds.ThreadSafeDatastore = blocksDS
	var _ ds.ThreadSafeDatastore = r.leveldbDS
	r.ds = ds2.ClaimThreadSafe{mountDS}
	return nil
}

func configureEventLoggerAtRepoPath(c *config.Config, repoPath string) {
	eventlog.Configure(eventlog.LevelInfo)
	eventlog.Configure(eventlog.LdJSONFormatter)
	rotateConf := eventlog.LogRotatorConfig{
		Filename:   path.Join(repoPath, "logs", "events.log"),
		MaxSizeMB:  c.Log.MaxSizeMB,
		MaxBackups: c.Log.MaxBackups,
		MaxAgeDays: c.Log.MaxAgeDays,
	}
	eventlog.Configure(eventlog.OutputRotatingLogFile(rotateConf))
}

// Close closes the FSRepo, releasing held resources.
func (r *FSRepo) Close() error {
	packageLock.Lock()
	defer packageLock.Unlock()

	if r.closed {
		return errors.New("repo is closed")
	}

	if err := r.leveldbDS.Close(); err != nil {
		return err
	}

	// This code existed in the previous versions, but
	// EventlogComponent.Close was never called. Preserving here
	// pending further discussion.
	//
	// TODO It isn't part of the current contract, but callers may like for us
	// to disable logging once the component is closed.
	// eventlog.Configure(eventlog.Output(os.Stderr))

	r.closed = true
	if err := r.lockfile.Close(); err != nil {
		return err
	}
	return nil
}

// Config returns the FSRepo's config. This method must not be called if the
// repo is not open.
//
// Result when not Open is undefined. The method may panic if it pleases.
func (r *FSRepo) Config() *config.Config {

	// It is not necessary to hold the package lock since the repo is in an
	// opened state. The package lock is _not_ meant to ensure that the repo is
	// thread-safe. The package lock is only meant to guard againt removal and
	// coordinate the lockfile. However, we provide thread-safety to keep
	// things simple.
	packageLock.Lock()
	defer packageLock.Unlock()

	if r.closed {
		panic("repo is closed")
	}
	return r.config
}

// setConfigUnsynced is for private use.
func (r *FSRepo) setConfigUnsynced(updated *config.Config) error {
	configFilename, err := config.Filename(r.path)
	if err != nil {
		return err
	}
	// to avoid clobbering user-provided keys, must read the config from disk
	// as a map, write the updated struct values to the map and write the map
	// to disk.
	var mapconf map[string]interface{}
	if err := serialize.ReadConfigFile(configFilename, &mapconf); err != nil {
		return err
	}
	m, err := config.ToMap(updated)
	if err != nil {
		return err
	}
	for k, v := range m {
		mapconf[k] = v
	}
	if err := serialize.WriteConfigFile(configFilename, mapconf); err != nil {
		return err
	}
	*r.config = *updated // copy so caller cannot modify this private config
	return nil
}

// SetConfig updates the FSRepo's config.
func (r *FSRepo) SetConfig(updated *config.Config) error {

	// packageLock is held to provide thread-safety.
	packageLock.Lock()
	defer packageLock.Unlock()

	return r.setConfigUnsynced(updated)
}

// GetConfigKey retrieves only the value of a particular key.
func (r *FSRepo) GetConfigKey(key string) (interface{}, error) {
	packageLock.Lock()
	defer packageLock.Unlock()

	if r.closed {
		return nil, errors.New("repo is closed")
	}

	filename, err := config.Filename(r.path)
	if err != nil {
		return nil, err
	}
	var cfg map[string]interface{}
	if err := serialize.ReadConfigFile(filename, &cfg); err != nil {
		return nil, err
	}
	return common.MapGetKV(cfg, key)
}

// SetConfigKey writes the value of a particular key.
func (r *FSRepo) SetConfigKey(key string, value interface{}) error {
	packageLock.Lock()
	defer packageLock.Unlock()

	if r.closed {
		return errors.New("repo is closed")
	}

	filename, err := config.Filename(r.path)
	if err != nil {
		return err
	}
	switch v := value.(type) {
	case string:
		if i, err := strconv.Atoi(v); err == nil {
			value = i
		}
	}
	var mapconf map[string]interface{}
	if err := serialize.ReadConfigFile(filename, &mapconf); err != nil {
		return err
	}
	if err := common.MapSetKV(mapconf, key, value); err != nil {
		return err
	}
	conf, err := config.FromMap(mapconf)
	if err != nil {
		return err
	}
	if err := serialize.WriteConfigFile(filename, mapconf); err != nil {
		return err
	}
	return r.setConfigUnsynced(conf) // TODO roll this into this method
}

// Datastore returns a repo-owned datastore. If FSRepo is Closed, return value
// is undefined.
func (r *FSRepo) Datastore() ds.ThreadSafeDatastore {
	packageLock.Lock()
	d := r.ds
	packageLock.Unlock()
	return d
}

var _ io.Closer = &FSRepo{}
var _ repo.Repo = &FSRepo{}

// IsInitialized returns true if the repo is initialized at provided |path|.
func IsInitialized(path string) bool {
	// packageLock is held to ensure that another caller doesn't attempt to
	// Init or Remove the repo while this call is in progress.
	packageLock.Lock()
	defer packageLock.Unlock()

	return isInitializedUnsynced(path)
}

// private methods below this point. NB: packageLock must held by caller.

// isInitializedUnsynced reports whether the repo is initialized. Caller must
// hold the packageLock.
func isInitializedUnsynced(repoPath string) bool {
	if !configIsInitialized(repoPath) {
		return false
	}
	if !util.FileExists(path.Join(repoPath, leveldbDirectory)) {
		return false
	}
	return true
}
