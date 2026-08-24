package deploy

import _ "embed"

// RuntimeInstaller 由签名 control 制品提供，避免向导引用未发布脚本。
//
//go:embed install-runtime.sh
var RuntimeInstaller []byte
