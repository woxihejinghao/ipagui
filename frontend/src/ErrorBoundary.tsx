import { Component, type ErrorInfo, type ReactNode } from "react";

type Props = {
    children: ReactNode;
};

type State = {
    error: Error | null;
};

/**
 * 最后一道保险。
 *
 * React 19 遇到渲染期抛出的异常会把整棵树卸载掉 —— 窗口只剩 `#f6f7f9` 的底色，
 * 用户看到的是「界面没了 / 一片空白」，而进程还活着、Go 侧没有任何日志可查。
 * 所以这里把崩溃收拢成一张卡片：看得见原因、能一键重来，比纯白好排查得多。
 */
export default class ErrorBoundary extends Component<Props, State> {
    state: State = { error: null };

    static getDerivedStateFromError(error: Error): State {
        return { error };
    }

    componentDidCatch(error: Error, info: ErrorInfo) {
        // 双击启动的 .app 没有 devtools，控制在 stderr 里也留一条痕迹。
        console.error("界面渲染失败", error, info.componentStack);
    }

    render() {
        const { error } = this.state;
        if (!error) {
            return this.props.children;
        }

        return (
            <div className="app">
                <div className="card" style={{ margin: 24 }}>
                    <h3>界面出错了</h3>
                    <p className="muted">
                        这是渲染期的异常，界面被 React 整体兜住了才没有变成一片空白。把下面这段贴给开发者，
                        或者先点「重新加载」——只是界面重来，账号和下载任务都不受影响。
                    </p>
                    <pre className="mono" style={{ whiteSpace: "pre-wrap", marginTop: 12 }}>
                        {error.message}
                        {error.stack ? `\n\n${error.stack}` : ""}
                    </pre>
                    <div className="row" style={{ marginTop: 16 }}>
                        <button className="primary" onClick={() => window.location.reload()}>
                            重新加载
                        </button>
                    </div>
                </div>
            </div>
        );
    }
}
