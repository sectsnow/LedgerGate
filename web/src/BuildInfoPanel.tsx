import type { BackendBuild } from "./api";

export function buildDescription(build?: BackendBuild | null): string {
  if (!build) return "后端版本尚未确认";
  return `后端 v${build.version}\n提交：${build.revision || "未提供"}\n构建时间：${build.built_at || "未注入"}${build.modified ? "\n包含未提交的源码修改" : ""}`;
}

export default function BuildInfoPanel({ build, frontend }: { build?: BackendBuild | null; frontend: string }) {
  return <section className="panel">
    <div className="panel-heading"><div><h2>运行版本</h2><p>核对当前后端构建，确认 NAS 更新是否生效。</p></div></div>
    <div className="form-grid">
      <div><small className="field-hint">后端版本</small><p>{build ? `v${build.version}` : "尚未确认"}</p></div>
      <div><small className="field-hint">前端版本</small><p>v{frontend}</p></div>
      <div><small className="field-hint">后端提交</small><p><code style={{ overflowWrap: "anywhere" }}>{build?.revision || "未提供"}</code></p></div>
      <div><small className="field-hint">构建时间</small><p>{build?.built_at ? new Date(build.built_at).toLocaleString("zh-CN", { hour12: false }) : "未注入"}</p></div>
    </div>
    {build?.modified && <p className="field-hint">此构建包含未提交的源码修改，提交号不能完整代表其代码内容。</p>}
    {build && build.version !== frontend && <p className="form-error">前后端版本不一致，请刷新页面并核对部署。</p>}
  </section>;
}
