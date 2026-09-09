package credentials

// ExpectedRef 是平台**预期存在**的一条凭据引用：运营页据此显示「还缺哪把」。
//
// 清单是固定的，不从登记簿推导：登记簿里的 credential_ref 是运营登记出来的，
// 而这五条是连接器与归档链路在代码里写死要读的引用——缺了哪条，对应的
// real 模式就一个请求都发不出去。
type ExpectedRef struct {
	Ref      string
	Platform string
	Purpose  string
}

// ExpectedRefs 返回固定清单（顺序稳定，供 Query 直接输出）。
func ExpectedRefs() []ExpectedRef {
	return []ExpectedRef{
		{Ref: "secret://sub2api-prod/read-token", Platform: "sub2api", Purpose: "Sub2API admin x-api-key"},
		{Ref: "secret://newapi/readonly-token", Platform: "newapi", Purpose: "NewAPI 管理员 access token"},
		{Ref: "secret://newapi/revenue-db", Platform: "newapi", Purpose: "NewAPI 收入库只读口令"},
		{Ref: "secret://archive/minio-runtime", Platform: "archive", Purpose: "归档 MinIO 运行时凭据"},
		{Ref: "secret://archive/minio-kms", Platform: "archive", Purpose: "归档 MinIO SSE-S3 密钥"},
		{Ref: "secret://alerts/wecom-webhook", Platform: "alerts", Purpose: "企业微信群机器人 Webhook 地址（含 key，视为凭据）"},
	}
}
