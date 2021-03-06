// aligot compiles C++ packages for ALICE
package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/gonuts/logger"
	"gopkg.in/yaml.v2"
)

var (
	cfg = Config{
		njobs:   1,
		disable: make(map[string]struct{}),
	}
	msg = logger.New("aligot")
)

type Config struct {
	action      string
	pkgs        []string
	cfgdir      string
	devel       []string
	docker      string
	wdir        string
	arch        string
	env         []string
	volumes     []string
	njobs       int
	refsrc      string
	remoteStore string
	writeStore  string
	disable     map[string]struct{}
	defaults    string
	debug       bool
}

type Spec struct {
	Package           string            `yaml:"package"`
	Version           string            `yaml:"version"`
	Requires          []string          `yaml:"requires"`
	BuildRequires     []string          `yaml:"build_requires"`
	RuntimeRequires   []string          `yaml:"runtime_requires"`
	Env               map[string]string `yaml:"env"`
	Source            string            `yaml:"source"`
	CommitHash        string            `yaml:"commit_hash"`
	WriteRepo         string            `yaml:"write_repo"`
	Tag               string            `yaml:"tag"`
	Recipe            string            `yaml:"recipe"`
	IncrementalRecipe string            `yaml:"incremental_recipe"`
	Hash              string            `yaml:"hash"`
	Revision          string            `yaml:"revision"`

	tar struct {
		storePath string
		linksPath string
		hashDir   string
		linkDir   string
	}
}

type Builder struct {
	cfg   Config
	pkgs  []string
	specs map[string]*Spec
	order []string
	sdir  string
}

func main() {
	var (
		err          error
		flagCfgDir   = flag.String("c", "alidist", "configuration directory")
		flagDevel    = flag.String("devel", "", "comma-separated list of development packages")
		flagDocker   = flag.Bool("docker", false, "enable/disable build in a docker container")
		flagWorkDir  = flag.String("w", "sw", "work directory")
		flagArch     = flag.String("a", "", "architecture to build for")
		flagEnv      = flag.String("e", "", "environment for the build")
		flagVols     = flag.String("v", "", "volumes for the docker-based build")
		flagJobs     = flag.Int("j", 1, "number of build jobs to cary in parallel")
		flagRefSrc   = flag.String("reference-sources", "sw/MIRROR", "")
		flagRemote   = flag.String("remote-store", "", "where to find packages already built for reuse")
		flagWrite    = flag.String("write-store", "", "where to upload the built packages for reuse. Use ssh:// in front for remote store.")
		flagDisable  = flag.String("disable", "", "comma-separated list of packages (and all of their (unique) dependencies) to NOT build")
		flagDefaults = flag.String("defaults", "release", "specify which defaults to use")
		flagDebug    = flag.Bool("d", false, "enable/disable debug outputs")
	)

	flag.Parse()

	if flag.NArg() != 2 {
		flag.Usage()
		os.Exit(2)
	}

	if *flagDisable != "" {
		for _, v := range strings.Split(*flagDisable, ",") {
			v = strings.TrimSpace(v)
			cfg.disable[v] = struct{}{}
		}
	}
	cfg.debug = *flagDebug
	cfg.action = flag.Arg(0)
	cfg.pkgs = []string{flag.Arg(1)}
	cfg.cfgdir = *flagCfgDir
	if *flagDevel != "" {
		for _, v := range strings.Split(*flagDevel, ",") {
			cfg.devel = append(
				cfg.devel,
				strings.TrimSpace(v),
			)
		}
	}
	if *flagEnv != "" {
		// FIXME(sbinet) handle escapes
		for _, v := range strings.Split(*flagEnv, ",") {
			cfg.env = append(
				cfg.env,
				v,
			)
		}
	}

	if *flagVols != "" {
		for _, v := range strings.Split(*flagVols, ",") {
			cfg.volumes = append(
				cfg.volumes,
				v,
			)
		}
	}

	cfg.wdir, err = filepath.Abs(*flagWorkDir)
	if err != nil {
		msg.Fatalf("could not resolve absolute path for [%s]: %v\n",
			*flagWorkDir,
			err,
		)
	}

	cfg.arch = *flagArch
	if *flagDocker {
		cfg.docker = fmt.Sprintf(
			"alisw/%s-builder",
			strings.Split(cfg.arch, "_")[0],
		)
	}

	cfg.njobs = *flagJobs
	cfg.refsrc = *flagRefSrc

	cfg.remoteStore = *flagRemote
	cfg.writeStore = *flagWrite

	cfg.remoteStore = strings.TrimPrefix(cfg.remoteStore, "ssh://")
	cfg.writeStore = strings.TrimPrefix(cfg.writeStore, "ssh://")

	if strings.HasSuffix(cfg.remoteStore, "::rw") {
		if len(cfg.writeStore) > 0 {
			msg.Fatalf(
				"you can NOT specify '::rw' and -write-store at the same time",
			)
		}
		cfg.remoteStore = strings.TrimSuffix(cfg.remoteStore, "::rw")
		cfg.writeStore = cfg.remoteStore
	}

	if len(cfg.devel) > 0 {
		msg.Infof("write store disabled since -devel option passed")
		msg.Infof("dev-packages: %v\n", cfg.devel)
		cfg.writeStore = ""
	}

	cfg.defaults = *flagDefaults

	if cfg.debug {
		msg.SetLevel(logger.DEBUG)
	}

	switch cfg.action {
	case "build":
		// ok
	default:
		msg.Fatalf("action [%s] unsupported\n", cfg.action)
	}

	b := Builder{
		cfg:   cfg,
		pkgs:  []string{cfg.pkgs[0]},
		specs: make(map[string]*Spec),
		sdir:  filepath.Join(cfg.wdir, "SPECS"),
	}
	err = os.MkdirAll(b.sdir, 0755)
	if err != nil {
		msg.Fatalf("could not create spec-dir [%s]: %v\n",
			b.sdir,
			err,
		)
	}

	msg.Debugf("using aligot recipes in %[1]sdist@%[2]s\n",
		"ali", hashDirectory(cfg.cfgdir),
	)

	pkgs := []string{cfg.pkgs[0]}
	for len(pkgs) > 0 {
		pkg := pkgs[0]
		pkgs = pkgs[1:]
		if _, ok := b.specs[pkg]; ok {
			continue
		}
		fname := filepath.Join(cfg.cfgdir, strings.ToLower(pkg)) + ".sh"
		buf, err := ioutil.ReadFile(fname)
		if err != nil {
			msg.Fatalf("could not read file [%s]: %v\n",
				fname,
				err,
			)
		}
		tokens := bytes.Split(buf, []byte("---"))
		hdr := tokens[0]
		recipe := tokens[1]

		var spec Spec
		err = yaml.Unmarshal(hdr, &spec)
		if err != nil {
			msg.Fatalf("could not unmarshal YAML document [%s]: %v\n",
				fname,
				err,
			)
		}

		if _, ok := cfg.disable[spec.Package]; ok {
			continue
		}

		// ATM, treat BuildRequires just as requires.
		fn := func(args []string) []string {
			archs := filterByArch(cfg.arch, args)
			o := make([]string, 0, len(archs))
			for _, v := range archs {
				if _, ok := cfg.disable[v]; !ok {
					o = append(o, v)
				}
			}
			return o
		}
		spec.Requires = fn(spec.Requires)
		spec.BuildRequires = fn(spec.BuildRequires)
		if spec.Package != "defaults-"+cfg.defaults {
			spec.BuildRequires = append(spec.BuildRequires,
				"defaults-"+cfg.defaults,
			)
		}
		spec.RuntimeRequires = make([]string, len(spec.Requires))
		copy(spec.RuntimeRequires, spec.Requires)
		spec.Requires = append([]string{}, spec.RuntimeRequires...)
		spec.Requires = append(spec.Requires, spec.BuildRequires...)
		if spec.Tag == "" {
			spec.Tag = spec.Version
		}
		spec.Version = strings.Replace(spec.Version, "/", "_", -1)

		msg.Debugf("spec[%s]: %v\n", pkg, spec.Requires)
		spec.Recipe = string(recipe)
		b.specs[spec.Package] = &spec
		pkgs = append(pkgs, spec.Requires...)
	}

	b.order = topoSort(b.specs)
	msg.Debugf("build order: %v\n", b.order)

	// resolve the tag to the actual commit ref
	for _, pkg := range b.order {
		spec := b.specs[pkg]
		spec.CommitHash = "0"
		if spec.Source != "" {
			// TODO(sbinet)

			spec.CommitHash = spec.Tag
		}

	}

	// decide what is the main package we are building and at what commit.
	//
	// we emit an event for the main package, when encountered, so that we can
	// use it to index builds of the same hash on different architectures.
	// we also make sure to add the main package and its hash to the debug log
	// so that we can always extract it from that log.
	// if one of the special packages is in the list of packages to be built, we
	// use it as main package rather than the last one.
	mainPkg := b.order[len(b.order)-1]
	mainPkgs := map[string]struct{}{
		"aliroot":    struct{}{},
		"aliphysics": struct{}{},
		"o2":         struct{}{},
	}
	hasMainPkgs := []string{}
	for i := len(b.order) - 1; i >= 0; i-- {
		v := b.order[i]
		low := strings.ToLower(v)
		if _, ok := mainPkgs[low]; ok {
			hasMainPkgs = append(hasMainPkgs, v)
		}
	}
	if len(hasMainPkgs) > 0 {
		mainPkg = hasMainPkgs[len(hasMainPkgs)-1]
	}
	mainHash := b.specs[mainPkg].CommitHash

	msg.Debugf("main package is %s@%s\n", mainPkg, mainHash)

	// now that we have the main package set, we can print out useful
	// informations which we will be able to associate with this build.
	for _, p := range b.order {
		spec := b.specs[p]
		if spec.Source != "" {
			msg.Debugf("commit hash for %s@%s is %s\n",
				spec.Source,
				spec.Tag,
				spec.CommitHash,
			)
		}
	}

	// calculate the hashes.
	// we do this in build order so that we can guarantee that the hashes of the
	// dependencies are calculated first.
	// also notice that if the commit hash is a real hash, and not a tag, we can
	// safely assume that's unique and therefore we can avoid putting the
	// repository or the name of the branch in the hash.
	msg.Debugf("calculating hashes.\n")
	for _, p := range b.order {
		spec := b.specs[p]
		hash := sha1.New()
		fct := func(s string) []byte {
			if s == "" {
				s = "none"
			}
			return []byte(s)
		}
		hash.Write(fct(spec.Recipe))
		hash.Write(fct(spec.Version))
		hash.Write(fct(spec.Package))
		hash.Write(fct(spec.CommitHash))
		// FIXME(sbinet)
		//hash.write(fct(spec.Env))
		//hash.Write(fct(spec.AppendPath))
		//hash.Write(fct(spec.PrependPath))
		//...

		spec.Hash = hex.EncodeToString(hash.Sum(nil))
		msg.Debugf("hash for recipe %s is %s\n", p, spec.Hash)
	}

	// this adds to the spec where it should find, localy or remotely, the
	// various tarballs and links.
	for _, p := range b.order {
		spec := b.specs[p]
		prefix := string(spec.Hash[:2])
		join := filepath.Join
		spec.tar.storePath = join("TARS", cfg.arch, "store", prefix, spec.Hash)
		spec.tar.linkDir = join("TARS", cfg.arch, spec.Package)
		spec.tar.hashDir = join(cfg.wdir, "TARS", cfg.arch, "store", prefix, cfg.arch)
		spec.tar.linkDir = join(cfg.wdir, "TARS", cfg.arch, spec.Package)

	}

	// we recursively calculate the full set of requires FullRequires,
	// including BuildRequires and the subset of them which are needed at
	// runtime: FullRuntimeRequires.
	// FIXME(sbinet)

	msg.Debugf("build order: %v\n", b.order)

	// we now iterate on all the packages, making sure we build correctly every
	// single one of them.
	// this is done this way so that the second time we run we can check if the
	// build was consistent and if it is, we bail out.
	niter := make(map[string]int)
	build := b.order
	for len(build) > 0 {
		p := build[0]
		build = build[1:]
		niter[p]++
		if niter[p] > 20 {
			msg.Fatalf(
				"too many attempts at building %s. Something wrong with the repository?\n",
				p,
			)
		}
		spec := b.specs[p]
		msg.Debugf(">>> %v...\n", spec.Package)

		// since we can execute this multiple times for a given package, in
		// order to ensure consistency, we need to reset things and make them
		// pristine.
		spec.Revision = ""

		msg.Debugf("updating from tarballs...\n")

		// if we arrived here, it really means we have a tarball which was
		// created using the same recipe.
		// we will still perform the build process rather than executing the
		// build itself.
		// we will:
		//  - unpack it in a temporary place
		//  - invoke the relocation specifying the correct workdir and the
		//    correct path which should have been used
		//  - move the version directory to its final destination, including the
		//    correct revision
		//  - repack it and put it in the store with the rest
		//
		// this will result in a new package which has the same binary contents
		// of the old one but where the relocation will work for the new
		// directory.
		// here, we simply store the fact that we can reuse the contents of
		// cached-tarball.
		if *flagRemote != "" {
			msg.Debugf("updating remote store for package %s@%s\n",
				spec.Package, spec.Hash,
			)
			panic("not implemented")
		}

		// decide how it should be called, based on the hash and what is already
		// available
		msg.Debugf("checking for packages already built...\n")
	}
}

func hashDirectory(dir string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		msg.Fatalf("error running 'git %v': %v\n",
			strings.Join(cmd.Args, " "),
			err,
		)
	}
	return string(bytes.TrimSuffix(out, []byte("\n")))
}

func filterByArch(arch string, reqs []string) []string {
	o := make([]string, 0, len(reqs))
	for _, v := range reqs {
		var (
			req     string
			matcher *regexp.Regexp
		)
		if strings.Index(v, ":") > -1 {
			s := strings.SplitN(v, ":", 1)
			req = s[0]
			matcher = regexp.MustCompile(s[1])
		} else {
			req = v
			matcher = regexp.MustCompile(".*")
		}
		if matcher.MatchString(arch) {
			o = append(o, req)
		}
	}
	return o
}

// topoSort does a topological sort to have the correct build order.
//
// adapted from gopl.io/ch5/toposort
func topoSort(m map[string]*Spec) []string {
	var order []string
	seen := make(map[string]bool)
	var visitAll func(items []string)

	visitAll = func(items []string) {
		for _, item := range items {
			if !seen[item] {
				seen[item] = true
				visitAll(m[item].Requires)
				order = append(order, item)
			}
		}
	}

	var keys []string
	for key := range m {
		keys = append(keys, key)
	}

	sort.Strings(keys)
	visitAll(keys)
	return order
}
