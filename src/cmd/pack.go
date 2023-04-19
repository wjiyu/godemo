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
	"sync"
	"time"
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

const (
	maxFileSize = 4 * 1024 * 1024 // 4MB
	numWorkers  = 4               // number of workers in the thread pool
)

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
	// create a wait group to wait for all workers to finish
	var wg sync.WaitGroup

	// create a channel to receive file paths
	filePaths := make(chan string)

	// create a channel to receive arrays of file paths
	filePathArrays := make(chan []string)

	// create a channel to signal when all workers have finished
	done := make(chan bool)

	// start the workers
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go worker(src, dst, filePathArrays, &wg)
	}

	// start the file path extractor
	go extractFilePaths(src, filePaths)

	// create a slice to hold file paths
	var filePathSlice []string

	// create a variable to hold the total size of the files in the slice
	var totalSize int64

	// create a ticker to periodically check the size of the slice
	ticker := time.NewTicker(time.Second)

	// loop over the file paths received from the extractor
	for filePath := range filePaths {
		//log.Println("file path: %s", filePath)
		// get the size of the file
		fileInfo, err := os.Stat(filePath)
		if err != nil {
			log.Printf("Error getting file info for %s: %s", filePath, err)
			continue
		}
		fileSize := fileInfo.Size()

		// if adding the file would exceed the max size, send the slice to the workers
		if totalSize+fileSize > int64(maxSize*1024*1024) {
			// send the slice to the workers
			filePathArrays <- filePathSlice

			// create a new slice to hold file paths
			filePathSlice = []string{filePath}

			// reset the total size
			totalSize = fileSize
		} else {
			// add the file path to the slice
			filePathSlice = append(filePathSlice, filePath)

			// add the file size to the total size
			totalSize += fileSize
		}

		// check if the ticker has ticked
		select {
		case <-ticker.C:
			fmt.Println("tick: %v", ticker.C)
			// do nothing
		default:
			fmt.Println("default")
			// do nothing
		}

		log.Println("filePathSlice: %v", filePathSlice)
	}

	// send the final slice to the workers
	filePathArrays <- filePathSlice

	//log.Println("file: %v", filePathSlice)

	// close the file path arrays channel
	close(filePathArrays)

	// wait for all workers to finish
	go func() {
		wg.Wait()
		done <- true
	}()

	// wait for all workers to finish or for a timeout
	select {
	case <-done:
		fmt.Println("All workers finished")
	case <-time.After(60 * time.Second):
		fmt.Println("Timeout waiting for workers to finish")
	}
}

//func packFolder(src, dst string, maxSize int) {
//	dirPath := src
//	dir, err := os.Open(dirPath)
//	if err != nil {
//		panic(err)
//	}
//	defer dir.Close()
//
//	// Create a tar writer
//	tarPath := dst
//	tarFile, err := os.Create(tarPath)
//	if err != nil {
//		panic(err)
//	}
//	defer tarFile.Close()
//
//	tarWriter := tar.NewWriter(tarFile)
//	defer tarWriter.Close()
//
//	var maxPackSize int64 = int64(maxSize * 1024 * 1024) // 4MB
//
//	var currentTarFileSize int64 = 0
//	var currentTarFileIndex int = 0
//	var currentTarFile *os.File = nil
//	defer func() {
//		if currentTarFile != nil {
//			currentTarFile.Close()
//		}
//	}()
//
//	var currentTarWriter *tar.Writer = nil
//
//	defer func() {
//		if currentTarWriter != nil {
//			currentTarWriter.Close()
//		}
//	}()
//
//	// Walk through the directory and add files to the tar archive
//	err = filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
//		if err != nil {
//			logger.Error(err)
//			return err
//		}
//
//		// Skip directories
//		if info.IsDir() {
//			return nil
//		}
//
//		log.Println("path: %s", path)
//
//		// Open the file to be added to the archive
//		file, err := os.Open(path)
//		if err != nil {
//			log.Println(err)
//			return err
//		}
//		defer file.Close()
//
//		// Create a new tar header
//		header := &tar.Header{
//			Name: path,
//			Mode: int64(info.Mode()),
//			Size: info.Size(),
//		}
//
//		currentTarFileSize += info.Size()
//
//		if currentTarFileSize > maxPackSize {
//
//			if tarWriter != nil {
//				if err := tarWriter.Close(); err != nil {
//					log.Println(err)
//					return err
//				}
//				tarWriter = nil
//			}
//
//			if currentTarWriter != nil {
//				if err := currentTarWriter.Close(); err != nil {
//					return err
//				}
//				currentTarWriter = nil
//				currentTarFileIndex++
//			}
//
//			currentTarFileSize = 0
//
//			currentTarFilePath := dst[:strings.Index(dst, ".tar")] + "_" + strconv.Itoa(currentTarFileIndex) + ".tar"
//			currentTarFile, err = os.Create(currentTarFilePath)
//			if err != nil {
//				return err
//			}
//			currentTarWriter = tar.NewWriter(currentTarFile)
//		}
//
//		if tarWriter != nil {
//			//fmt.Println("tar: %v", tarWriter)
//			// Write the header to the tar archive
//			if err := tarWriter.WriteHeader(header); err != nil {
//				log.Println(err)
//				return err
//			}
//
//			// Copy the file to the tar archive
//			if _, err := io.Copy(tarWriter, file); err != nil {
//				log.Println(err)
//				return err
//			}
//		}
//
//		if currentTarWriter != nil {
//			if err := currentTarWriter.WriteHeader(header); err != nil {
//				log.Println(err)
//				return err
//			}
//
//			if _, err := io.Copy(currentTarWriter, file); err != nil {
//				log.Println(err)
//				return err
//			}
//		}
//
//		return nil
//	})
//
//	if err != nil {
//		log.Println(err)
//	}
//
//	log.Println("Tar archives created successfully.")
//}

func extractFilePaths(dirPath string, filePaths chan<- string) {
	// walk the directory tree
	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("Error walking path %s: %s", path, err)
			return nil
		}

		// if the path is a file, send it to the channel
		if !info.IsDir() {
			//log.Println("path: %v", path)
			filePaths <- path
		}

		return nil
	})

	if err != nil {
		log.Printf("Error walking directory %s: %s", dirPath, err)
	}

	// close the file paths channel
	close(filePaths)
}

func worker(src, dst string, filePathArrays <-chan []string, wg *sync.WaitGroup) {
	// loop over the file path arrays received from the channel
	for filePathArray := range filePathArrays {
		fmt.Println("path array: %v", filePathArray)
		// create a tar file
		tarFile, err := os.CreateTemp(dst, "tar")
		if err != nil {
			log.Printf("Error creating tar file: %s", err)
			continue
		}

		// create a new tar writer
		tarWriter := tar.NewWriter(tarFile)

		// loop over the file paths in the array
		for _, filePath := range filePathArray {
			// open the file
			file, err := os.Open(filePath)
			if err != nil {
				log.Printf("Error opening file %s: %s", filePath, err)
				continue
			}

			// get the file info
			fileInfo, err := file.Stat()
			if err != nil {
				log.Printf("Error getting file info for %s: %s", filePath, err)
				continue
			}

			// create a new header for the file
			header := &tar.Header{
				Name:    strings.TrimPrefix(filePath, filepath.Join(filepath.Dir(src), "/")),
				Size:    fileInfo.Size(),
				Mode:    int64(fileInfo.Mode()),
				ModTime: fileInfo.ModTime(),
			}

			// write the header to the tar file
			err = tarWriter.WriteHeader(header)
			if err != nil {
				log.Printf("Error writing header for %s: %s", filePath, err)
				continue
			}

			// copy the file contents to the tar file
			_, err = io.Copy(tarWriter, file)
			if err != nil {
				log.Printf("Error copying file %s to tar file: %s", filePath, err)
				continue
			}

			// close the file
			err = file.Close()
			if err != nil {
				log.Printf("Error closing file %s: %s", filePath, err)
				continue
			}
		}

		// close the tar writer
		err = tarWriter.Close()
		if err != nil {
			log.Printf("Error closing tar writer: %s", err)
			continue
		}

		// close the tar file
		err = tarFile.Close()
		if err != nil {
			log.Printf("Error closing tar file: %s", err)
			continue
		}

		// remove the file path array from the channel
		<-filePathArrays
	}

	// signal that the worker has finished
	wg.Done()
}
