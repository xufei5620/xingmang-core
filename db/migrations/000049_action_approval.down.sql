-- 先删 decision：它引用 request，反过来删会撞外键。
DROP TABLE IF EXISTS core.approval_decision;
DROP TABLE IF EXISTS core.approval_request;
