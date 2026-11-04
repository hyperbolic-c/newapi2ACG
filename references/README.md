# 上游源码参考

用户已将参考仓库放在项目根目录下的 `new-api/`；不再使用原定的 `references/new-api/`。

用户指定仓库：`git@github.com:QuantumNous/new-api.git`

用户指定 tag：`v1.0.0-rc.21`

已核验 commit：`bde9b2f44887d34ec54799ae191d50f97914359e`。该目录只读参考，未修改上游源码。

可从项目根目录复核：

```sh
git -C new-api rev-parse HEAD
git -C new-api describe --tags --exact-match
git -C new-api status --short
```

核验结果见 `docs/compatibility/new-api.md`，字段清单见 `docs/compatibility/new-api-schema.md`。参考仓库从迁移工具的 Git 追踪中排除，避免将上游完整工程作为嵌套仓库误提交。
