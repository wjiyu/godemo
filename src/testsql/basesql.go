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
	Files   []string `xorm:"blob notnull"`
	Name    []byte   `xorm:"varbinary(255) notnull"`
}

func Init() *xorm.Engine {
	engine, err := xorm.NewEngine("mysql", "root:w995219@tcp(10.151.11.61:3306)/juicefs2?charset=utf8mb4")
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
