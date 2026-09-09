package localauth

// ScopeManage 是管理员工账号（创建、改角色、启停、重置密码、查看清单）所需
// 的权限。
//
// 不复用 credential.manage / connector.manage：管理"谁能登录管理后台"与
// "谁能改上游凭据/连接器模式"是两件事，爆炸半径不同——前者出错会让人登不
// 进来或被冒充，后者出错会让 worker 连错上游或明文落错地方。默认只由
// admin 角色持有（见 oidcauth.DefaultRoleScopeMap 的 XM-LOGIN 注释），
// staff 不含它。
const ScopeManage = "staff.manage"
