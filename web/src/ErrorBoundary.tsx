import { Component, type ErrorInfo, type ReactNode } from 'react';

type Props = { children: ReactNode };
type State = { error?: Error };

// Keep a runtime failure readable instead of letting React unmount to the bare
// page background. The browser console still has the original stack.
export class ErrorBoundary extends Component<Props, State> {
  state: State = {};

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('Unhandled UI error', error, info);
  }

  render() {
    if (!this.state.error) return this.props.children;
    return <div className="app-error-boundary" role="alert">
      <h1>页面加载失败</h1>
      <p>{this.state.error.message || '未知错误'}</p>
      <p className="app-error-boundary-hint">请刷新页面重试；如果问题持续，请查看浏览器控制台中的错误详情。</p>
      <button type="button" onClick={() => window.location.reload()}>重新加载</button>
    </div>;
  }
}
