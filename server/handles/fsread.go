package handles

import (
	"fmt"
	stdpath "path"
	"strings"
	"time"

	"github.com/alist-org/alist/v3/internal/conf"
	"github.com/alist-org/alist/v3/internal/errs"
	"github.com/alist-org/alist/v3/internal/fs"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/alist-org/alist/v3/internal/setting"
	"github.com/alist-org/alist/v3/internal/sign"
	"github.com/alist-org/alist/v3/pkg/utils"
	"github.com/alist-org/alist/v3/server/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

type ListReq struct {
	model.PageReq
	Path     string `json:"path" form:"path"`
	Password string `json:"password" form:"password"`
	Refresh  bool   `json:"refresh"`
}

type DirReq struct {
	Path      string `json:"path" form:"path"`
	Password  string `json:"password" form:"password"`
	ForceRoot bool   `json:"force_root" form:"force_root"`
}

type ObjResp struct {
	Id          string                     `json:"id"`
	Path        string                     `json:"path"`
	Name        string                     `json:"name"`
	Size        int64                      `json:"size"`
	IsDir       bool                       `json:"is_dir"`
	Modified    time.Time                  `json:"modified"`
	Created     time.Time                  `json:"created"`
	Sign        string                     `json:"sign"`
	Thumb       string                     `json:"thumb"`
	Type        int                        `json:"type"`
	HashInfoStr string                     `json:"hashinfo"`
	HashInfo    map[*utils.HashType]string `json:"hash_info"`
}

type FsListResp struct {
	Content  []ObjLabelResp `json:"content"`
	Total    int64          `json:"total"`
	Readme   string         `json:"readme"`
	Header   string         `json:"header"`
	Write    bool           `json:"write"`
	Provider string         `json:"provider"`
}

type ObjLabelResp struct {
	Id          string                     `json:"id"`
	Path        string                     `json:"path"`
	Name        string                     `json:"name"`
	Size        int64                      `json:"size"`
	IsDir       bool                       `json:"is_dir"`
	Modified    time.Time                  `json:"modified"`
	Created     time.Time                  `json:"created"`
	Sign        string                     `json:"sign"`
	Thumb       string                     `json:"thumb"`
	Type        int                        `json:"type"`
	HashInfoStr string                     `json:"hashinfo"`
	HashInfo    map[*utils.HashType]string `json:"hash_info"`
	LabelList   []model.Label              `json:"label_list"`
}

func FsList(c *gin.Context) {
	var req ListReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	req.Validate()
	user := c.MustGet("user").(*model.User)
	reqPath, err := user.JoinPath(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	meta, err := op.GetNearestMeta(reqPath)
	if err != nil {
		if !errors.Is(errors.Cause(err), errs.MetaNotFound) {
			common.ErrorResp(c, err, 500, true)
			return
		}
	}
	c.Set("meta", meta)
	if !common.CanAccessWithRoles(user, meta, reqPath, req.Password) {
		common.ErrorStrResp(c, "password is incorrect or you have no permission", 403)
		return
	}
	perm := common.MergeRolePermissions(user, reqPath)
	if !common.HasPermission(perm, common.PermWrite) && !common.CanWrite(meta, reqPath) && req.Refresh {
		common.ErrorStrResp(c, "Refresh without permission", 403)
		return
	}
	objs, err := fs.List(c, reqPath, &fs.ListArgs{Refresh: req.Refresh})
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	total, objs := pagination(objs, &req.PageReq)
	provider := "unknown"
	storage, err := fs.GetStorage(reqPath, &fs.GetStoragesArgs{})
	if err == nil {
		provider = storage.GetStorage().Driver
	}
	common.SuccessResp(c, FsListResp{
		Content:  toObjsResp(objs, reqPath, isEncrypt(meta, reqPath), user.ID),
		Total:    int64(total),
		Readme:   getReadme(meta, reqPath),
		Header:   getHeader(meta, reqPath),
		Write:    common.HasPermission(perm, common.PermWrite) || common.CanWrite(meta, reqPath),
		Provider: provider,
	})
}

func FsDirs(c *gin.Context) {
	var req DirReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	user := c.MustGet("user").(*model.User)
	reqPath := req.Path
	if req.ForceRoot {
		if !user.IsAdmin() {
			common.ErrorStrResp(c, "Permission denied", 403)
			return
		}
	} else {
		tmp, err := user.JoinPath(req.Path)
		if err != nil {
			common.ErrorResp(c, err, 403)
			return
		}
		reqPath = tmp
	}
	meta, err := op.GetNearestMeta(reqPath)
	if err != nil {
		if !errors.Is(errors.Cause(err), errs.MetaNotFound) {
			common.ErrorResp(c, err, 500, true)
			return
		}
	}
	c.Set("meta", meta)
	if !common.CanAccessWithRoles(user, meta, reqPath, req.Password) {
		common.ErrorStrResp(c, "password is incorrect or you have no permission", 403)
		return
	}
	objs, err := fs.List(c, reqPath, &fs.ListArgs{})
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	dirs := filterDirs(objs)
	common.SuccessResp(c, dirs)
}

type DirResp struct {
	Name     string    `json:"name"`
	Modified time.Time `json:"modified"`
}

func filterDirs(objs []model.Obj) []DirResp {
	var dirs []DirResp
	for _, obj := range objs {
		if obj.IsDir() {
			dirs = append(dirs, DirResp{
				Name:     obj.GetName(),
				Modified: obj.ModTime(),
			})
		}
	}
	return dirs
}

func getReadme(meta *model.Meta, path string) string {
	if meta != nil && (utils.PathEqual(meta.Path, path) || meta.RSub) {
		return meta.Readme
	}
	return ""
}

func getHeader(meta *model.Meta, path string) string {
	if meta != nil && (utils.PathEqual(meta.Path, path) || meta.HeaderSub) {
		return meta.Header
	}
	return ""
}

func isEncrypt(meta *model.Meta, path string) bool {
	if common.IsStorageSignEnabled(path) {
		return true
	}
	if meta == nil || meta.Password == "" {
		return false
	}
	if !utils.PathEqual(meta.Path, path) && !meta.PSub {
		return false
	}
	return true
}

func pagination(objs []model.Obj, req *model.PageReq) (int, []model.Obj) {
	pageIndex, pageSize := req.Page, req.PerPage
	total := len(objs)
	start := (pageIndex - 1) * pageSize
	if start > total {
		return total, []model.Obj{}
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return total, objs[start:end]
}

func toObjsResp(objs []model.Obj, parent string, encrypt bool, userId uint) []ObjLabelResp {
	var resp []ObjLabelResp
	for _, obj := range objs {
		var labels []model.Label
		if obj.IsDir() == false {
			labels, _ = op.GetLabelByFileName(userId, obj.GetName())
		}
		thumb, _ := model.GetThumb(obj)
		resp = append(resp, ObjLabelResp{
			Id:          obj.GetID(),
			Path:        obj.GetPath(),
			Name:        obj.GetName(),
			Size:        obj.GetSize(),
			IsDir:       obj.IsDir(),
			Modified:    obj.ModTime(),
			Created:     obj.CreateTime(),
			HashInfoStr: obj.GetHash().String(),
			HashInfo:    obj.GetHash().Export(),
			Sign:        common.Sign(obj, parent, encrypt),
			Thumb:       thumb,
			Type:        utils.GetObjType(obj.GetName(), obj.IsDir()),
			LabelList:   labels,
		})
	}
	return resp
}

type FsGetReq struct {
	Path     string `json:"path" form:"path"`
	Password string `json:"password" form:"password"`
}

type FsGetResp struct {
	ObjResp
	RawURL   string         `json:"raw_url"`
	Readme   string         `json:"readme"`
	Header   string         `json:"header"`
	Provider string         `json:"provider"`
	Related  []ObjLabelResp `json:"related"`
}

func FsGet(c *gin.Context) {
	var req FsGetReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	user := c.MustGet("user").(*model.User)
	reqPath, err := user.JoinPath(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	meta, err := op.GetNearestMeta(reqPath)
	if err != nil {
		if !errors.Is(errors.Cause(err), errs.MetaNotFound) {
			common.ErrorResp(c, err, 500)
			return
		}
	}
	c.Set("meta", meta)
	if !common.CanAccessWithRoles(user, meta, reqPath, req.Password) {
		common.ErrorStrResp(c, "password is incorrect or you have no permission", 403)
		return
	}
	obj, err := fs.Get(c, reqPath, &fs.GetArgs{})
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	var rawURL string

	storage, err := fs.GetStorage(reqPath, &fs.GetStoragesArgs{})
	provider := "unknown"
	if err == nil {
		provider = storage.Config().Name
	}
	if !obj.IsDir() {
		if err != nil {
			common.ErrorResp(c, err, 500)
			return
		}
		if storage.Config().MustProxy() || storage.GetStorage().WebProxy {
			query := ""
			if isEncrypt(meta, reqPath) || setting.GetBool(conf.SignAll) {
				query = "?sign=" + sign.Sign(reqPath)
			}
			if storage.GetStorage().DownProxyUrl != "" {
				rawURL = fmt.Sprintf("%s%s?sign=%s",
					strings.Split(storage.GetStorage().DownProxyUrl, "\n")[0],
					utils.EncodePath(reqPath, true),
					sign.Sign(reqPath))
			} else {
				rawURL = fmt.Sprintf("%s/p%s%s",
					common.GetApiUrl(c.Request),
					utils.EncodePath(reqPath, true),
					query)
			}
		} else {
			// file have raw url
			if url, ok := model.GetUrl(obj); ok {
				rawURL = url
			} else {
				// if storage is not proxy, use raw url by fs.Link
				link, _, err := fs.Link(c, reqPath, model.LinkArgs{
					IP:       c.ClientIP(),
					Header:   c.Request.Header,
					HttpReq:  c.Request,
					Redirect: true,
				})
				if err != nil {
					common.ErrorResp(c, err, 500)
					return
				}
				rawURL = link.URL
			}
		}
	}
	var related []model.Obj
	parentPath := stdpath.Dir(reqPath)
	sameLevelFiles, err := fs.List(c, parentPath, &fs.ListArgs{})
	if err == nil {
		related = filterRelated(sameLevelFiles, obj)
	}
	parentMeta, _ := op.GetNearestMeta(parentPath)
	thumb, _ := model.GetThumb(obj)
	common.SuccessResp(c, FsGetResp{
		ObjResp: ObjResp{
			Id:          obj.GetID(),
			Path:        obj.GetPath(),
			Name:        obj.GetName(),
			Size:        obj.GetSize(),
			IsDir:       obj.IsDir(),
			Modified:    obj.ModTime(),
			Created:     obj.CreateTime(),
			HashInfoStr: obj.GetHash().String(),
			HashInfo:    obj.GetHash().Export(),
			Sign:        common.Sign(obj, parentPath, isEncrypt(meta, reqPath)),
			Type:        utils.GetFileType(obj.GetName()),
			Thumb:       thumb,
		},
		RawURL:   rawURL,
		Readme:   getReadme(meta, reqPath),
		Header:   getHeader(meta, reqPath),
		Provider: provider,
		Related:  toObjsResp(related, parentPath, isEncrypt(parentMeta, parentPath), user.ID),
	})
}

func filterRelated(objs []model.Obj, obj model.Obj) []model.Obj {
	var related []model.Obj
	nameWithoutExt := strings.TrimSuffix(obj.GetName(), stdpath.Ext(obj.GetName()))
	for _, o := range objs {
		if o.GetName() == obj.GetName() {
			continue
		}
		if strings.HasPrefix(o.GetName(), nameWithoutExt) {
			related = append(related, o)
		}
	}
	return related
}

type FsOtherReq struct {
	model.FsOtherArgs
	Password string `json:"password" form:"password"`
}

func FsOther(c *gin.Context) {
	var req FsOtherReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	user := c.MustGet("user").(*model.User)
	var err error
	req.Path, err = user.JoinPath(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	meta, err := op.GetNearestMeta(req.Path)
	if err != nil {
		if !errors.Is(errors.Cause(err), errs.MetaNotFound) {
			common.ErrorResp(c, err, 500)
			return
		}
	}
	c.Set("meta", meta)
	if !common.CanAccessWithRoles(user, meta, req.Path, req.Password) {
		common.ErrorStrResp(c, "password is incorrect or you have no permission", 403)
		return
	}
	res, err := fs.Other(c, req.FsOtherArgs)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, res)
}
