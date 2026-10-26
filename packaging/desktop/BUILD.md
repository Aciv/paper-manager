# 构建与发行

普通用户只需阅读 [使用说明](USER.md)。本页面向维护者；项目代码使用 MIT，
第三方组件保留各自协议，见 [许可说明](THIRD_PARTY_NOTICES.md)。
旧安装器与受管理升级的设计归档在 [packaging/archive](../archive/README.md)。

## 本机制作包

以下命令只构建执行命令所在平台：Linux x86_64、Windows x86_64、macOS arm64。
Go 可以交叉编译，但 Python 解释器、二进制依赖和 PostgreSQL 需要在目标平台构建及验证。
构建机需要 Go（版本见 go.mod）、Node 22、Python 3.12；Unix 另需 C 编译器、
make、bison、flex、Perl；macOS 需要系统 SDK。用户机器无需安装这些工具。

```bash
python scripts/desktop/build_desktop_bundle.py \
  --output tmp/desktop-bundle --cache tmp/desktop-downloads \
  --revision "$(git rev-parse HEAD)" --profile hybrid
python scripts/desktop/package_desktop_bundle.py \
  --bundle tmp/desktop-bundle --output tmp/releases/desktop.tar.gz
```

Windows 使用 `.zip` 输出；Unix 使用 `.tar.gz` 保留执行权限和符号链接。
输出位置必须不存在。下载缓存会按锁文件校验；不复制开发环境、凭据或用户数据。
`hybrid` 包含已授权的 header/citation 两个 CRF 小模型；`rules` 是独立降级配置。

用户解压后看到原生入口、`bundle/`、`skills/`、`.launcher_env`、README 和当前平台的卸载入口。
首次运行生成同目录 `data/`。运行组件与许可位于 bundle 内；安装器、测试程序、
内部报告不放进用户包。归档旁的 SHA/manifest 文件供构建与发行工具验证，用户无需操作。

## GitHub Actions

只有两个手动入口，另有一个被复用的平台任务：

- **Full tests**（test.yml）：完整 Go/数据库测试，三平台本机构建、Python/前端测试、
  精简包导出，以及在另一台干净 runner 上的真实启动、解析、配置与卸载测试。
  不对每个普通提交自动运行；可手动选择分支运行，或推送 `validation/test-*` 标签。
- **Release**（release.yml）：要求所选提交有成功的 Full tests，下载那个运行留下的三平台
  `paper_manager-*` 产物，再核对 revision、版本和校验值，创建 draft Release。
  不重新构建；人工检查草稿后发布。修改过代码必须重新完整测试。

测试运行页的 Artifacts 下可下载 `paper_manager-ubuntu-22.04`、
`paper_manager-windows-2022`、`paper_manager-macos-14`，保留 7 天。
平台测试失败时不会将该平台标记为可用下载；失败日志在 `test-results-*`。
Release 必须在产物过期之前执行。原生构建中间候选包不代表通过验收。

手动工作流文件需要进入仓库默认分支，才会出现在 Actions 的手动入口中。
每个包记录实际检出的 github.sha，并要求与测试运行和 Release 的提交完全一致。
