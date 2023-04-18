package cmd

import (
	"archive/tar"
	"demo/src/utils"
	"fmt"
	"github.com/google/gops/agent"
	"github.com/pyroscope-io/client/pyroscope"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

var logger = utils.GetLogger("wjy")

func setup(c *cli.Context, n int) {
	if c.NArg() < n {
		fmt.Printf("ERROR: This command requires at least %d arguments\n", n)
		fmt.Printf("USAGE:\n   juicefs %s [command options] %s\n", c.Command.Name, c.Command.ArgsUsage)
		os.Exit(1)
	}

	if c.Bool("trace") {
		utils.SetLogLevel(logrus.TraceLevel)
	} else if c.Bool("verbose") {
		utils.SetLogLevel(logrus.DebugLevel)
	} else if c.Bool("quiet") {
		utils.SetLogLevel(logrus.WarnLevel)
	} else {
		utils.SetLogLevel(logrus.InfoLevel)
	}
	if c.Bool("no-color") {
		utils.DisableLogColor()
	}

	if !c.Bool("no-agent") {
		go func() {
			for port := 6060; port < 6100; port++ {
				_ = http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", port), nil)
			}
		}()
		go func() {
			for port := 6070; port < 6100; port++ {
				_ = agent.Listen(agent.Options{Addr: fmt.Sprintf("127.0.0.1:%d", port)})
			}
		}()
	}

	if c.IsSet("pyroscope") {
		tags := make(map[string]string)
		appName := fmt.Sprintf("juicefs.%s", c.Command.Name)
		if c.Command.Name == "mount" {
			tags["mountpoint"] = c.Args().Get(1)
		}
		if hostname, err := os.Hostname(); err == nil {
			tags["hostname"] = hostname
		}
		tags["pid"] = strconv.Itoa(os.Getpid())
		tags["version"] = "v1"

		if _, err := pyroscope.Start(pyroscope.Config{
			ApplicationName: appName,
			ServerAddress:   c.String("pyroscope"),
			Logger:          logger,
			Tags:            tags,
			AuthToken:       os.Getenv("PYROSCOPE_AUTH_TOKEN"),
			ProfileTypes:    pyroscope.DefaultProfileTypes,
		}); err != nil {
			logger.Errorf("start pyroscope agent: %v", err)
		}
	}
}

func CmdPack() *cli.Command {
	return &cli.Command{
		Name:      "pack",
		Action:    pack,
		Category:  "TOOL",
		Usage:     "package small file data sets",
		ArgsUsage: "SOURCE PATH AND DEST PATH",
		Description: `
It is used to package the raw small file data set to the storage system.

Examples:
$ juicefs pack /home/wjy/imagenet /mnt/jfs`,
		Flags: []cli.Flag{
			&cli.UintFlag{
				Name:    "pack-size",
				Aliases: []string{"s"},
				Value:   4,
				Usage:   "size of each pack in MiB(max size 4MB)",
			},
		},
	}
}

func pack(ctx *cli.Context) error {
	setup(ctx, 2)
	if runtime.GOOS == "windows" {
		logger.Infof("Windows is not supported")
		return nil
	}

	if ctx.Uint("pack-size") == 0 || ctx.Uint("pack-size") > 4 {
		return os.ErrInvalid
	}

	src := ctx.Args().Get(0)
	dst := ctx.Args().Get(1)

	if src == dst {
		return os.ErrInvalid
	}

	packFolder(src, dst, int(ctx.Uint("pack-size")))

	return nil
}

func packFolder(src, dst string, maxSize int) {
	dirPath := src
	dir, err := os.Open(dirPath)
	if err != nil {
		panic(err)
	}
	defer dir.Close()

	// Create a tar writer
	tarPath := dst
	tarFile, err := os.Create(tarPath)
	if err != nil {
		panic(err)
	}
	defer tarFile.Close()

	tarWriter := tar.NewWriter(tarFile)
	defer tarWriter.Close()

	var maxPackSize int64 = int64(maxSize * 1024 * 1024) // 4MB

	var currentTarFileSize int64 = 0
	var currentTarFileIndex int = 0
	var currentTarFile *os.File = nil
	defer func() {
		if currentTarFile != nil {
			currentTarFile.Close()
		}
	}()

	var currentTarWriter *tar.Writer = nil

	defer func() {
		if currentTarWriter != nil {
			currentTarWriter.Close()
		}
	}()

	// Walk through the directory and add files to the tar archive
	err = filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			logger.Error(err)
			return err
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		log.Println("path: %s", path)

		// Open the file to be added to the archive
		file, err := os.Open(path)
		if err != nil {
			log.Println(err)
			return err
		}
		defer file.Close()

		// Create a new tar header
		header := &tar.Header{
			Name: path,
			Mode: int64(info.Mode()),
			Size: info.Size(),
		}

		currentTarFileSize += info.Size()

		if currentTarFileSize > maxPackSize {

			if tarWriter != nil {
				if err := tarWriter.Close(); err != nil {
					log.Println(err)
					return err
				}
				tarWriter = nil
			}

			if currentTarWriter != nil {
				if err := currentTarWriter.Close(); err != nil {
					return err
				}
				currentTarWriter = nil
				currentTarFileIndex++
			}

			currentTarFileSize = 0

			currentTarFilePath := dst[:strings.Index(dst, ".tar")] + "_" + strconv.Itoa(currentTarFileIndex) + ".tar"
			currentTarFile, err = os.Create(currentTarFilePath)
			if err != nil {
				return err
			}
			currentTarWriter = tar.NewWriter(currentTarFile)
		}

		if tarWriter != nil {
			//fmt.Println("tar: %v", tarWriter)
			// Write the header to the tar archive
			if err := tarWriter.WriteHeader(header); err != nil {
				log.Println(err)
				return err
			}

			// Copy the file to the tar archive
			if _, err := io.Copy(tarWriter, file); err != nil {
				log.Println(err)
				return err
			}
		}

		if currentTarWriter != nil {
			if err := currentTarWriter.WriteHeader(header); err != nil {
				log.Println(err)
				return err
			}

			if _, err := io.Copy(currentTarWriter, file); err != nil {
				log.Println(err)
				return err
			}
		}

		return nil
	})

	if err != nil {
		log.Println(err)
	}

	log.Println("Tar archives created successfully.")
}
