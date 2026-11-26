package appstore

// 本文件是 GUI 仓库对 ipatool fork 的本地测试辅助，不属于上游。
//
// 为什么需要它：fork 保留了「ent/download 成功但没有 FairPlay 数据就继续走老链路」
// 这处补丁（见 third_party/ipatool/FORK.md「修复内容 1」）。上游测试把
// 「成功但 Items[0].Sinfs 为空」当作合法成功响应，于是补丁会把它们判成
// 「需要回退」，测试里就会多出期望之外的请求。
//
// 现实中的成功下载响应是带 sinf 的，所以本地把这几处 fixture 补上 sinf，
// 让上游测试重新表达「真实成功响应」。同步上游时如果 kbsync_test.go 的
// 这三处 fixture 上下文变了，按 FORK.md 的同步说明重放即可。

// testFairPlaySinfs 返回一份最小的、非空的 FairPlay 许可证数据。
func testFairPlaySinfs() []Sinf {
	return []Sinf{{ID: 1, Data: []byte("sinf")}}
}
