// aligot compiles C++ packages for ALICE
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

var (
	cfg = Config{njobs: 1}
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
	disable     []string
	debug       bool
}

func main() {
	var (
		err         error
		flagCfgDir  = flag.String("c", "*dist", "configuration directory")
		flagDevel   = flag.String("devel", "", "comma-separated list of development packages")
		flagDocker  = flag.Bool("docker", false, "enable/disable build in a docker container")
		flagWorkDir = flag.String("w", "sw", "work directory")
		flagArch    = flag.String("a", "", "architecture to build for")
		flagEnv     = flag.String("e", "", "environment for the build")
		flagVols    = flag.String("v", "", "volumes for the docker-based build")
		flagJobs    = flag.Int("j", 1, "number of build jobs to cary in parallel")
		flagRefSrc  = flag.String("reference-sources", "sw/MIRROR", "")
		flagRemote  = flag.String("remote-store", "",
			"where to find packages already built for reuse")
		flagWrite = flag.String("write-store", "",
			"where to upload the built packages for reuse. Use ssh:// in front for remote store.")
		flagDisable = flag.String("disable", "",
			"comma-separated list of packages (and all of their (unique) dependencies) to NOT build")
		flagDebug = flag.Bool("d", false, "enable/disable debug outputs")
	)

	flag.Parse()

	if flag.NArg() != 2 {
		flag.Usage()
		os.Exit(2)
	}

	if *flagDisable != "" {
		cfg.disable = strings.Split(*flagDisable, ",")
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
		log.Fatalf("could not resolve absolute path for [%s]: %v\n",
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
			log.Fatalf(
				"you can NOT specify '::rw' and -write-store at the same time",
			)
		}
		cfg.remoteStore = strings.TrimSuffix(cfg.remoteStore, "::rw")
		cfg.writeStore = cfg.remoteStore
	}

	if len(cfg.devel) > 0 {
		log.Printf("write store disabled since -devel option passed")
		log.Printf("dev-packages: %v\n", cfg.devel)
		cfg.writeStore = ""
	}

	switch cfg.action {
	case "build":
		// ok
	default:
		log.Fatalf("action [%s] unsupported\n", cfg.action)
	}

}
