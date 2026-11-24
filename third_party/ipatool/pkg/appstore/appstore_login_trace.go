// 【fork 补丁 4：认证响应追踪，默认关闭，见 FORK.md】
//
// 登录失败时上游只给一句英文，现场信息（HTTP 状态、苹果的 customerMessage、
// 这一轮用的是哪套机器头）全被丢掉，国内网络下排查 2FA 失败特别费劲。
// 设置环境变量 IPATOOL_AUTH_TRACE=<日志文件路径> 之后，每次认证往返追加一行 JSON。
//
// 记录内容刻意不含任何凭据：密码、验证码、passwordToken、cookie、机器头的值都不写，
// 只写「有没有」和长度。
package appstore

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/majd/ipatool/v2/pkg/http"
)

const authTraceEnv = "IPATOOL_AUTH_TRACE"

// traceAuthenticationAttempt 把一次认证往返的结果追加到 IPATOOL_AUTH_TRACE 指定的文件。
// 任何写入错误都被吞掉：追踪不能影响登录本身。
func (t *appstore) traceAuthenticationAttempt(res http.Result[loginResult], attempt int, authCode string, responseErr error) {
	path := strings.TrimSpace(os.Getenv(authTraceEnv))
	if path == "" {
		return
	}

	amdAction, _ := res.GetHeader("X-Apple-AMD-Action")
	amdData, _ := res.GetHeader("X-Apple-AMD-Data")
	location, _ := res.GetHeader("location")
	dsid, _ := res.GetHeader("X-Dsid")

	entry := map[string]any{
		"time":             time.Now().Format(time.RFC3339Nano),
		"attempt":          attempt,
		"authCodeSent":     authCode != "",
		"status":           res.StatusCode,
		"failureType":      res.Data.FailureType,
		"customerMessage":  res.Data.CustomerMessage,
		"hasPasswordToken": res.Data.PasswordToken != "",
		"hasDSID":          dsid != "" || res.Data.DirectoryServicesID != "",
		"amdAction":        amdAction,
		"amdDataLength":    len(amdData),
		"location":         location,
		"amdHeader":        t.authenticationHeaders.Get("X-Apple-I-MD") != "",
		"amdMHeader":       t.authenticationHeaders.Get("X-Apple-I-MD-M") != "",
		"rinfoHeader":      t.authenticationHeaders.Get("X-Apple-I-MD-RINFO") != "",
		"headerCount":      len(t.authenticationHeaders),
	}

	if responseErr != nil {
		entry["error"] = responseErr.Error()
	}

	data, marshalErr := json.Marshal(entry)
	if marshalErr != nil {
		return
	}

	file, openErr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if openErr != nil {
		return
	}
	defer func() { _ = file.Close() }()

	_, _ = file.Write(append(data, '\n'))
}
