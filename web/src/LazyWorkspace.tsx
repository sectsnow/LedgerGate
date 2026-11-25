import { Component, type ReactNode } from "react";

export default class LazyWorkspace extends Component<{children:ReactNode},{failed:boolean}> {
  state={failed:false};
  static getDerivedStateFromError(){return {failed:true};}
  render(){
    return this.state.failed ? <section className="panel dashboard-onboarding" role="alert"><h2>统计页面加载失败</h2><p>请重新加载后重试，服务器上已保存的数据不受影响。</p><button className="secondary" onClick={()=>location.reload()}>重新加载页面</button></section> : this.props.children;
  }
}
