package utils

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

const (
	pipe       = "│   "
	tee        = "├── "
	lasttee    = "└── "
	defaultDir = "."
)

type FileNode struct {
	Level    int
	FileName string
	IsDir    bool
	Children []*FileNode
	Parent   *FileNode
	Left     *FileNode
	Right    *FileNode
}

// ShowTree prints out the contents of the tree, using its prefix to determine the proper indentation and the difference
// between the tee and lasttee characters to denote the beginning or end of a tree branch
func (node *FileNode) ShowTree(prefix string) {
	if node.Level == 0 {
		fmt.Println(defaultDir)
	}

	var subFix string
	if node.Right != nil {
		subFix = tee
	} else {
		subFix = lasttee
	}

	if node.FileName != "" {
		fmt.Printf("%s%s%s\n", prefix, subFix, node.FileName)
	}

	if node.IsDir {
		if node.Right != nil {
			prefix += pipe
		} else {
			prefix += "    "
		}

	}

	for _, child := range node.Children {
		child.ShowTree(prefix)
	}
}

// MTree recursively walks through the file/directory path, creating a FileNode for each item and connecting it to its parent and siblings accordingly.
func (node *FileNode) MTree(path string) error {
	filePaths, err := os.ReadDir(path)
	if err != nil {
		log.Println(err)
		return err
	}

	if len(filePaths) == 0 {
		return nil
	}

	node.Children = make([]*FileNode, 0, len(filePaths))
	var pre *FileNode = nil
	for _, filePath := range filePaths {
		if strings.HasPrefix(filePath.Name(), ".") {
			continue
		}
		childFile := &FileNode{
			Level:    node.Level + 1,
			Children: nil,
			FileName: filePath.Name(),
			Parent:   node,
			Left:     pre,
			IsDir:    filePath.IsDir(),
		}

		if pre != nil {
			pre.Right = childFile
		}
		pre = childFile
		node.Children = append(node.Children, childFile)
		if filePath.IsDir() {
			err = childFile.MTree(filepath.Join(path, filePath.Name()))
			if err != nil {
				log.Println(err)
				return err
			}
		}
	}

	return nil
}

func (node *FileNode) LTree(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	node.Children = make([]*FileNode, 0, len(paths))
	//var pre *FileNode = nil
	for _, path := range paths {
		names := strings.Split(path, "/")
		current := node
		for index, name := range names {
			child := current.GetChild(name, index != len(names)-1)
			current = child
			//if pre != nil {
			//	pre.Right = child
			//}
			//pre = child
		}
	}
	return nil
}

func (node *FileNode) GetChild(name string, isDir bool) *FileNode {
	for _, child := range node.Children {
		if child.FileName == name {
			return child
		}
	}

	var pre *FileNode = nil
	if len(node.Children) > 0 {
		pre = node.Children[len(node.Children)-1]
	}

	childFile := &FileNode{
		Level:    node.Level + 1,
		Children: nil,
		FileName: name,
		Parent:   node,
		Left:     pre,
		IsDir:    isDir,
	}

	if pre != nil {
		pre.Right = childFile
	}

	node.Children = append(node.Children, childFile)
	return childFile
}
