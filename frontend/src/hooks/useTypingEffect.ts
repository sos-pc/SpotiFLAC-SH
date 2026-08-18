import { useState, useEffect, useMemo } from 'react';
export function useTypingEffect(texts: string[], typingSpeed: number = 50, deletingSpeed: number = 50, pauseDuration: number = 1500) {
    // A set of texts is identified by its CONTENT, not by the array's identity.
    //
    // A caller that builds its list inline — `[...A, ...B]` — hands this hook a
    // new array on every render. Compared by identity, the reset below then
    // fired on every render; a render-phase setState that always fires is an
    // infinite loop, React gives up with "Too many re-renders" (#301), and the
    // whole page fails to mount. That shipped once, in #110, and the build could
    // not see it: tsc, eslint and vite all pass on it.
    //
    // \u0000 as the separator because no placeholder contains one. The joined
    // string is both the comparison key and what the effect depends on, so an
    // unstable argument can no longer churn either.
    const key = texts.join("\u0000");
    const items = useMemo(() => key.split("\u0000"), [key]);
    const [displayedText, setDisplayedText] = useState('');
    const [isDeleting, setIsDeleting] = useState(false);
    const [textIndex, setTextIndex] = useState(0);
    // Reset when the set changes, adjusted during render rather than via a
    // dedicated effect — see
    // https://react.dev/learn/you-might-not-need-an-effect#adjusting-some-state-when-a-prop-changes
    const [prevKey, setPrevKey] = useState(key);
    if (key !== prevKey) {
        setPrevKey(key);
        setDisplayedText("");
        setIsDeleting(false);
        setTextIndex(0);
    }
    useEffect(() => {
        // The setTimeout-chained typing/deleting animation genuinely can't
        // be expressed as a derived render-time value — it's a timer loop
        // that advances its own state machine across renders.
        const currentText = items[textIndex % items.length];
        let timer: ReturnType<typeof setTimeout>;
        if (isDeleting) {
            timer = setTimeout(() => {
                setDisplayedText((prev) => prev.substring(0, prev.length - 1));
            }, deletingSpeed);
        }
        else {
            timer = setTimeout(() => {
                setDisplayedText((prev) => currentText.substring(0, prev.length + 1));
            }, typingSpeed);
        }
        if (!isDeleting && displayedText === currentText) {
            clearTimeout(timer);
            timer = setTimeout(() => setIsDeleting(true), pauseDuration);
        }
        else if (isDeleting && displayedText === '') {
            // eslint-disable-next-line react-hooks/set-state-in-effect
            setIsDeleting(false);
            setTextIndex((prev) => (prev + 1) % items.length);
        }
        return () => clearTimeout(timer);
    }, [displayedText, isDeleting, textIndex, items, typingSpeed, deletingSpeed, pauseDuration]);
    return displayedText;
}
