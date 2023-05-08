package cmd

import (
	"archive/tar"
	"demo/src/utils"
	"fmt"
	"github.com/google/gops/agent"
	//"github.com/juicedata/juicefs/pkg/meta"
	"github.com/pyroscope-io/client/pyroscope"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"
)

var logger = utils.GetLogger("wjy")

type Counter struct {
	count uint64
	lock  sync.Mutex
}

func (c *Counter) Increment() {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.count++
}

func (c *Counter) Get() uint64 {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.count
}

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

var (
	mutex sync.Mutex
	count uint64
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
$ juicefs pack /home/wjy/imagenet /mnt/jfs -m "mysql://jfs:mypassword@(127.0.0.1:3306)/juicefs"
# A safer alternative
$ export META_PASSWORD=mypassword 
$ juicefs pack /home/wjy/imagenet /mnt/jfs -m "mysql://jfs:@(127.0.0.1:3306)/juicefs"`,
		Flags: []cli.Flag{
			&cli.UintFlag{
				Name:    "pack-size",
				Aliases: []string{"s"},
				Value:   4,
				Usage:   "size of each pack in MiB(max size 4MB)",
			},

			&cli.UintFlag{
				Name:    "works",
				Aliases: []string{"w"},
				Value:   5,
				Usage:   "number of concurrent threads in the thread pool(max number 20)",
			},

			&cli.StringFlag{
				Name:    "meta-url",
				Aliases: []string{"m"},
				Usage:   "META-URL is used to connect the metadata engine (Redis, TiKV, MySQL, etc.)",
			},

			&cli.StringFlag{
				Name:    "mount-point",
				Aliases: []string{"p"},
				Usage:   "mount path",
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

	if ctx.Uint("pack-size") <= 0 || ctx.Uint("pack-size") > 4 {
		return os.ErrInvalid
	}

	if ctx.Uint("works") <= 0 || ctx.Uint("works") > 20 {
		return os.ErrInvalid
	}

	//if ctx.String("meta-url") == "" {
	//	return os.ErrInvalid
	//}

	src := ctx.Args().Get(0)
	dst := ctx.Args().Get(1)

	if src == dst {
		return os.ErrInvalid
	}

	//p, err := filepath.Abs(src)
	//if err != nil {
	//	logger.Errorf("abs of %s: %s", src, err)
	//}
	//d := filepath.Dir(p)
	//name := filepath.Base(p)

	packChunk(ctx, filepath.Clean(src), filepath.Clean(dst))

	return nil
}

func packChunk(ctx *cli.Context, src, dst string) {
	// create a wait group to wait for all workers to finish
	var wg sync.WaitGroup

	//pack size
	maxChunkSize := int(ctx.Uint("pack-size"))
	//work numbers
	numWorkers := int(ctx.Uint("works"))

	// create a channel to receive file paths
	filePaths := make(chan string)

	// create a channel to receive arrays of file paths
	filePathArrays := make(chan []string)

	// create a channel to signal when all workers have finished
	done := make(chan bool)

	//meta client
	//metaUri := ctx.String("meta-url")
	//removePassword(metaUri)
	//m := meta.NewClient(metaUri, &meta.Config{Retries: 10, Strict: true, MountPoint: ctx.String("mount-point")})
	//_, err := m.Load(true)
	//if err != nil {
	//	logger.Fatalf("load setting: %s", err)
	//}

	// start the workers
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go worker("", src, dst, filePathArrays, &wg)
	}

	// scan data set paths
	go scanPaths(src, filePaths)

	// create a slice to hold file paths
	var filePathSlice []string

	// create a variable to hold the total size of the files in the slice
	var totalSize int64

	// create a ticker to periodically check the size of the slice
	ticker := time.NewTicker(time.Second)

	// loop over the file paths received from the scan
	go func() {
		for filePath := range filePaths {
			// get the size of the file
			fileInfo, err := os.Stat(filePath)
			if err != nil {
				logger.Errorf("Error getting file info for %s: %s", filePath, err)
				continue
			}
			fileSize := fileInfo.Size()

			// if adding the file would exceed the max size, send the slice to the workers
			if totalSize+fileSize > int64(maxChunkSize*1024*1024) {
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
				logger.Debugf("tick: %v", ticker.C)
				// do nothing
			default:
				//logger.Debugf("default")
				// do nothing
			}
		}

		// send the final slice to the workers
		filePathArrays <- filePathSlice

		// close the file path arrays channel
		close(filePathArrays)
	}()

	// wait for all workers to finish
	go func() {
		wg.Wait()
		done <- true
	}()

	// wait for all workers to finish or for a timeout
	select {
	case <-done:
		logger.Infof("All workers finished!")
	case <-time.After(60 * time.Second):
		logger.Infof("Timeout waiting for workers to finish")
	}
}

func scanPaths(dirPath string, filePaths chan<- string) {
	// walk the directory tree
	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			logger.Fatalf("Error walking path %s: %s", path, err)
			return nil
		}

		// if the path is a file, send it to the channel
		if !info.IsDir() {
			filePaths <- path
		}

		return nil
	})

	if err != nil {
		logger.Fatalf("Error walking directory %s: %s", dirPath, err)
	}

	// close the file paths channel
	close(filePaths)
}

func worker(m string, src, dst string, filePathArrays <-chan []string, wg *sync.WaitGroup) {
	// loop over the file path arrays received from the channel
	for filePathArray := range filePathArrays {
		//tar name
		var name string
		//logger.Debugf("path array: %v", filePathArray)
		// create a tar file
		//tarFile, err := os.CreateTemp(dst, "tar")
		var number uint64
		mutex.Lock()
		number = count
		count++
		mutex.Unlock()

		dstDir := dst + string(os.PathSeparator) + "pack"

		if _, err := os.Stat(dstDir); err != nil {
			err = os.MkdirAll(dstDir, os.ModePerm)
			if err != nil {
				logger.Error(err)
			}
		}

		tarName := dstDir + string(os.PathSeparator) + filepath.Base(src) + "_" + strconv.FormatUint(number, 10)

		tarFile, err := os.Create(tarName)
		if err != nil {
			logger.Errorf("Error creating tar file: %s", err)
			continue
		}

		name = tarFile.Name()

		// create a new tar writer
		tarWriter := tar.NewWriter(tarFile)

		// loop over the file paths in the array
		for _, filePath := range filePathArray {
			// open the file
			file, err := os.Open(filePath)
			if err != nil {
				logger.Errorf("Error opening file %s: %s", filePath, err)
				continue
			}

			// get the file info
			fileInfo, err := file.Stat()
			if err != nil {
				logger.Errorf("Error getting file info for %s: %s", filePath, err)
				continue
			}

			// create a new header for the file
			relativePath, _ := filepath.Rel(filepath.Dir(src), filePath)
			header := &tar.Header{
				Name:    relativePath,
				Size:    fileInfo.Size(),
				Mode:    int64(fileInfo.Mode()),
				ModTime: fileInfo.ModTime(),
			}

			// write the header to the tar file
			err = tarWriter.WriteHeader(header)
			if err != nil {
				logger.Errorf("Error writing header for %s: %s", filePath, err)
				continue
			}

			// copy the file contents to the tar file
			_, err = io.Copy(tarWriter, file)
			if err != nil {
				logger.Errorf("Error copying file %s to tar file: %s", filePath, err)
				continue
			}

			// close the file
			err = file.Close()
			if err != nil {
				logger.Errorf("Error closing file %s: %s", filePath, err)
				continue
			}
		}

		// close the tar writer
		err = tarWriter.Close()
		if err != nil {
			logger.Errorf("Error closing tar writer: %s", err)
			continue
		}

		// close the tar file
		err = tarFile.Close()
		if err != nil {
			logger.Errorf("Error closing tar file: %s", err)
			continue
		}

		// remove the file path array from the channel
		//<-filePathArrays

		//sync chunk file list info to table
		//err = meta.SyncChunkInfo(meta.Background, m, 0, name)
		if err != nil {
			logger.Errorf("sync chunk file info error: %s", err)
			continue
		}
		logger.Debugf("sync chunk files %s info finished!", name)
	}

	// signal that the worker has finished
	wg.Done()
}
