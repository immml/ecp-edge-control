package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
)

// fileItem 是文件列表条目，JSON 序列化后放进结果 stdout。
type fileItem struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	IsDir      bool   `json:"is_dir"`
	Size       int64  `json:"size"`
	Mode       string `json:"mode"`
	ModifiedAt string `json:"modified_at"`
}

// fileList 列出目录内容（COMMAND_TYPE_FILE_LIST）。
//
// 参数：params.path（默认 "/"）。
// 结果：stdout = JSON 数组 [ {name,path,is_dir,size,mode,modified_at}, ... ]。
// 只读操作，任何用户权限均可执行；目录不可读时返回明确错误。
func (e *Executor) fileList(cmd *ecpv1.Command) *ecpv1.CommandResult {
	path := getString(cmd.GetParams(), "path")
	if path == "" {
		path = "/"
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return e.fail(cmd, "读取目录失败: "+err.Error())
	}

	items := make([]fileItem, 0, len(entries))
	for _, de := range entries {
		it := fileItem{
			Name:  de.Name(),
			Path:  filepath.Join(path, de.Name()),
			IsDir: de.IsDir(),
		}
		if info, err := de.Info(); err == nil {
			it.Size = info.Size()
			it.Mode = info.Mode().String()
			it.ModifiedAt = info.ModTime().Format(time.RFC3339)
		}
		items = append(items, it)
	}
	data, err := json.Marshal(items)
	if err != nil {
		return e.fail(cmd, "序列化文件列表失败: "+err.Error())
	}

	r := e.base(cmd)
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
	r.Stdout = data
	return r
}
