package testsql

import (
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"github.com/go-xorm/xorm"
	"xorm.io/xorm/names"
)

var engine *xorm.Engine

type Ino uint64

type ChunkFile struct {
	Id      int64    `xorm:"pk bigserial"`
	Inode   Ino      `xorm:"unique(chunk_file) notnull"`
	ChunkId uint64   `xorm:"chunkid unique(chunk_file) notnull"`
	Files   []string `xorm:"blob "`
	Name    []byte   `xorm:"varbinary(255) "`
}

func (c *ChunkFile) TableName() string {
	return "jfs_chunk_file"
}

type Session2 struct {
	Sid    uint64 `xorm:"pk"`
	Expire int64  `xorm:"notnull"`
	Info   []byte `xorm:"blob"`
}

type Chunk struct {
	Id     int64  `xorm:"pk bigserial"`
	Inode  Ino    `xorm:"unique(chunk) notnull"`
	Indx   uint32 `xorm:"unique(chunk) notnull"`
	Slices []byte `xorm:"blob notnull"`
}

type Edge struct {
	Id     int64  `xorm:"pk bigserial"`
	Parent Ino    `xorm:"unique(edge) notnull"`
	Name   []byte `xorm:"unique(edge) varbinary(255) notnull"`
	Inode  Ino    `xorm:"index notnull"`
	Type   uint8  `xorm:"notnull"`
}

type Node struct {
	Inode  Ino    `xorm:"pk"`
	Type   uint8  `xorm:"notnull"`
	Flags  uint8  `xorm:"notnull"`
	Mode   uint16 `xorm:"notnull"`
	Uid    uint32 `xorm:"notnull"`
	Gid    uint32 `xorm:"notnull"`
	Atime  int64  `xorm:"notnull"`
	Mtime  int64  `xorm:"notnull"`
	Ctime  int64  `xorm:"notnull"`
	Nlink  uint32 `xorm:"notnull"`
	Length uint64 `xorm:"notnull"`
	Rdev   uint32
	Parent Ino
}

type NamedNode struct {
	//Node  `xorm:"extends"`
	Files []string `xorm:"blob"`
	Name  []byte   `xorm:"varbinary(255)"`
}

func Init() *xorm.Engine {
	engine, err := xorm.NewEngine("mysql", "root:w995219@tcp(10.151.11.61:3306)/juicefs3?charset=utf8mb4")
	if err != nil {
		panic(err.Error())
	}

	err = engine.Ping()
	if err != nil {
		fmt.Printf("connect ping failed: %v", err)
	}

	engine.SetTableMapper(names.NewPrefixMapper(engine.GetTableMapper(), "jfs_"))

	engine.ShowSQL(true)

	return engine

}

func CreateTableByEngine(engine *xorm.Engine) {
	if ok, _ := engine.IsTableExist(ChunkFile{}); ok {
		fmt.Println("table exists!")
	} else {
		err := engine.CreateTables(ChunkFile{})
		if err != nil {
			fmt.Println("create table failed!")
		} else {
			fmt.Println("create table success!")
		}
	}
}
