// Package response 提供统一的 HTTP 响应封装，确保所有接口返回一致的
// {code, message, data} 结构。
//
// 设计要点：业务错误码（body 中的 code）与 HTTP 传输状态码解耦。
// HTTP 状态行仍使用标准数字（200/400/401/...，属于传输层，语义正确），
// 而 body 中的 code 使用下方自定义的 5 位、按模块分段的业务错误码：
// 前两位 = 模块，后三位 = 模块内具体错误。前端 web/test.html 以 code === 0
// 判定成功，因此 CodeOK 固定为 0。
//
// ┌─────────────── 业务错误码目录 (Business Error Code Catalog) ───────────────┐
//
//	0      CodeOK                成功
//
//	── 通用/系统 (10xxx) ──
//	10001  CodeInvalidParam      参数错误
//	10002  CodeUnauthorized      未登录 / 凭证无效
//	10003  CodeForbidden         无权限（非管理员等）
//	10004  CodeNotFound          资源不存在
//	10005  CodeTooManyRequests   请求过于频繁（限流）
//	10500  CodeInternalError     服务器内部错误
//
//	── Auth 鉴权 (11xxx) ──
//	11001  CodeLoginFailed       飞书登录 / OAuth 失败
//	11002  CodeTokenExpired      飞书 token 过期 / 未授权
//
//	── Report 周报 (12xxx) ──
//	12001  CodeReportNotFound    周报不存在
//	12002  CodeReportExists      周报已存在（重复生成等）
//
//	── DataSource 数据源 (13xxx) ──
//	13001  CodeDataSourceNotFound 数据源不存在
//	13002  CodeDataSourceTestFail 数据源连通性测试失败
//
//	── Template 模板 (14xxx) ──
//	14001  CodeTemplateNotFound  模板不存在
//
//	── Collect/Export 采集与导出 (15xxx) ──
//	15001  CodeCollectFailed     采集失败
//	15002  CodeExportFailed      导出失败
//
//	── Reminder 提醒 (16xxx) ──
//	16001  CodeRemindFailed      提醒发送失败
//
// └───────────────────────────────────────────────────────────────────────────┘
package response

import "github.com/gin-gonic/gin"

// 业务错误码常量。CodeOK 表示成功（固定为 0，前端据此判定）。
// 其余为自定义 5 位、按模块分段的业务错误码（与 HTTP 状态码解耦）。
const (
	CodeOK = 0

	// 通用/系统 (10xxx)
	CodeInvalidParam    = 10001 // 参数错误
	CodeUnauthorized    = 10002 // 未登录 / 凭证无效
	CodeForbidden       = 10003 // 无权限（非管理员等）
	CodeNotFound        = 10004 // 资源不存在
	CodeTooManyRequests = 10005 // 请求过于频繁（限流）
	CodeInternalError   = 10500 // 服务器内部错误

	// Auth 鉴权 (11xxx)
	CodeLoginFailed  = 11001 // 飞书登录 / OAuth 失败
	CodeTokenExpired = 11002 // 飞书 token 过期 / 未授权

	// Report 周报 (12xxx)
	CodeReportNotFound = 12001 // 周报不存在
	CodeReportExists   = 12002 // 周报已存在（重复生成等）

	// DataSource 数据源 (13xxx)
	CodeDataSourceNotFound = 13001 // 数据源不存在
	CodeDataSourceTestFail = 13002 // 数据源连通性测试失败

	// Template 模板 (14xxx)
	CodeTemplateNotFound = 14001 // 模板不存在

	// Collect/Export 采集与导出 (15xxx)
	CodeCollectFailed = 15001 // 采集失败
	CodeExportFailed  = 15002 // 导出失败

	// Reminder 提醒 (16xxx)
	CodeRemindFailed = 16001 // 提醒发送失败
)

// OK 返回统一成功响应：{code:0, message:"ok", data:...}，HTTP 200。
// 保持 code/data 两个字段与前端 web/test.html 既有解析逻辑兼容。
func OK(c *gin.Context, data interface{}) {
	c.JSON(200, gin.H{"code": CodeOK, "message": "ok", "data": data})
}

// Fail 返回统一错误响应：{code, message, error, data:null}。
// httpStatus 为传输层 HTTP 状态码（标准数字），bizCode 为 body 中的业务错误码，
// 两者解耦。附带 error 别名字段，兼容前端中以 data.error 读取错误信息的代码。
func Fail(c *gin.Context, httpStatus, bizCode int, message string) {
	c.JSON(httpStatus, gin.H{"code": bizCode, "message": message, "error": message, "data": nil})
}

// FailParam 参数错误：HTTP 400 + 业务码 10001。
func FailParam(c *gin.Context, message string) {
	Fail(c, 400, CodeInvalidParam, message)
}

// FailUnauthorized 未登录/凭证无效：HTTP 401 + 业务码 10002。
func FailUnauthorized(c *gin.Context, message string) {
	Fail(c, 401, CodeUnauthorized, message)
}

// FailForbidden 无权限：HTTP 403 + 业务码 10003。
func FailForbidden(c *gin.Context, message string) {
	Fail(c, 403, CodeForbidden, message)
}

// FailNotFound 资源不存在：HTTP 404 + 业务码 10004。
func FailNotFound(c *gin.Context, message string) {
	Fail(c, 404, CodeNotFound, message)
}

// FailInternal 服务器内部错误：HTTP 500 + 业务码 10500。
func FailInternal(c *gin.Context, message string) {
	Fail(c, 500, CodeInternalError, message)
}
