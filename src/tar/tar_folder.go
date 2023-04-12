package tar

import (
	"archive/tar"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const maxFileSize = 4 * 1024 * 1024 // 4MB

func TarFile(src, dst string) {
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
			log.Println(err)
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

		if currentTarFileSize > maxFileSize {

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
