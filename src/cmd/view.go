package cmd

import (
	"demo/src/testsql"
	"demo/src/utils"
	"fmt"
	"github.com/urfave/cli/v2"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
)

var err error

func CmdView() *cli.Command {
	return &cli.Command{
		Name:      "view",
		Action:    view,
		Category:  "TOOL",
		Usage:     "displays the aggregated data set view",
		ArgsUsage: "",
		Description: `It is used to display the aggregated view of the data set.

Examples:
$ juicefs view /home/wjy/imagenet /mnt/jfs -m "mysql://jfs:mypassword@(127.0.0.1:3306)/juicefs"
# A safer alternative
$ export META_PASSWORD=mypassword 
$ juicefs view /home/wjy/imagenet /mnt/jfs -m "mysql://jfs:@(127.0.0.1:3306)/juicefs"`,

		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:    "tree",
				Aliases: []string{"t"},
				Usage:   "the tree structrue displays the view!",
			},

			&cli.BoolFlag{
				Name:    "list",
				Aliases: []string{"l"},
				Value:   true,
				Usage:   "display the view in list format",
			},

			&cli.StringFlag{
				Name:    "meta-url",
				Aliases: []string{"m"},
				Usage:   "META-URL is used to connect the metadata engine (Redis, TiKV, MySQL, etc.)",
			},
		},
	}
}

func view(ctx *cli.Context) error {
	setup(ctx, 1)
	if runtime.GOOS == "windows" {
		logger.Infof("Windows is not supported!")
		return nil
	}

	//if ctx.String("meta-url") == "" {
	//	return os.ErrInvalid
	//}

	path := ctx.Args().Get(0)

	var files []string
	if path == "" {
		path, err = os.Getwd()
		if err != nil {
			log.Println(err)
		}
	}

	fileInfo, err := os.Stat(path)
	if err != nil {
		log.Println(err)
	}

	var datasetName string
	if os.IsNotExist(err) {
		datasetName = filepath.Base(path)
		path = filepath.Dir(path)
		fileInfo, err = os.Stat(path)
		if err != nil {
			log.Println(err)
		}
	}

	stat, ok := fileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		log.Println("failed to get inode")
	}

	inode := stat.Ino

	engine := testsql.Init()

	var nodes []testsql.NamedNode
	s := engine.NewSession()
	if fileInfo.IsDir() {
		s = engine.Table(&testsql.Edge{})
		s = s.Join("INNER", &testsql.ChunkFile{}, "jfs_edge.inode = jfs_chunk_file.inode")
		if datasetName != "" {
			s = s.Where(" jfs_edge.name like ?", datasetName+"%")
		}
		if err := s.Find(&nodes, &testsql.Edge{Parent: testsql.Ino(inode)}); err != nil {
			log.Println(err)
		}
	} else {
		s = engine.Table(&testsql.ChunkFile{})
		if err := s.Find(&nodes, &testsql.ChunkFile{Inode: testsql.Ino(inode)}); err != nil {
			log.Println(err)
		}
	}

	//limit := -1
	//if limit > 0 {
	//	s = s.Limit(limit, 0)
	//}
	//

	for _, n := range nodes {
		//log.Printf("nodes: %v", n.Name)
		if len(n.Name) == 0 {
			logger.Errorf("Corrupt entry with empty name: inode %d parent %d", inode)
			continue
		}

		index := strings.LastIndex(string(n.Name), "_")
		if index < 0 {
			log.Println(index)
			continue
		}

		subName := string(n.Name)[:index]

		if datasetName == subName {
			files = append(files, n.Files...)
		}
	}

	//sort file list
	sort.Slice(files, func(i, j int) bool {
		dirI := filepath.Dir(files[i])
		dirJ := filepath.Dir(files[j])
		return dirI < dirJ
	})

	if ctx.Bool("list") && !ctx.Bool("tree") {
		for _, filePath := range files {
			fmt.Println(filePath)
		}
	}

	if ctx.Bool("tree") {
		node := &utils.FileNode{}
		node.LTree(files)
		node.ShowTree("")
	}

	return nil

}
