import React from 'react'
import {createRoot} from 'react-dom/client'
import './style.css'
import App from './App'
import ErrorBoundary from './ErrorBoundary'

// macOS 上 main.go 用的是 mac.TitleBarHiddenInset()：WebView 铺满整个窗口，
// 左上角「红绿灯」（关闭/最小化/缩放）浮在页面内容之上。把平台写进 <html data-os>，
// 样式表据此只在 macOS 给顶栏留出交通灯的空位；Windows 的原生标题栏在 WebView 之外，
// 不需要留白。用 UA 判断而不是 window.runtime，是为了首帧就到位、不闪一下。
const isMac = /Macintosh|Mac OS X/.test(navigator.userAgent)
document.documentElement.dataset.os = isMac ? 'macos' : 'other'

const container = document.getElementById('root')

const root = createRoot(container!)

root.render(
    <React.StrictMode>
        <ErrorBoundary>
            <App/>
        </ErrorBoundary>
    </React.StrictMode>
)
