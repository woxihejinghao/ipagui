import { useState } from "react";

type Props = {
    url?: string;
    size?: number;
    /** url 为空时的说明。 */
    missingTitle?: string;
};

/**
 * 应用图标。两个来源：搜索列表用 Apple 公开目录接口给的 artworkUrl，
 * 本地库用 IPA 包里的 iTunesArtwork（就是那一版的图标）。
 * 两处都可能没有 —— 用同尺寸的占位块顶上，行高不跟着图片有无跳动。
 */
export default function AppIcon({ url, size = 44, missingTitle = "没有图标" }: Props) {
    const [failed, setFailed] = useState(false);
    const style = { width: size, height: size };

    if (!url || failed) {
        return (
            <div
                className="icon icon-placeholder"
                style={style}
                title={failed ? "图标加载失败" : missingTitle}
            >
                —
            </div>
        );
    }

    return (
        <img className="icon" style={style} src={url} alt="" loading="lazy" onError={() => setFailed(true)} />
    );
}
