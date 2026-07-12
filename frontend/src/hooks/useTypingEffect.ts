import { useState, useEffect } from 'react';
export function useTypingEffect(texts: string[], typingSpeed: number = 50, deletingSpeed: number = 50, pauseDuration: number = 1500) {
    const [displayedText, setDisplayedText] = useState('');
    const [isDeleting, setIsDeleting] = useState(false);
    const [textIndex, setTextIndex] = useState(0);
    // Reset when the texts array itself changes (a new placeholder set),
    // adjusted during render rather than via a dedicated effect — see
    // https://react.dev/learn/you-might-not-need-an-effect#adjusting-some-state-when-a-prop-changes
    const [prevTexts, setPrevTexts] = useState(texts);
    if (texts !== prevTexts) {
        setPrevTexts(texts);
        setDisplayedText("");
        setIsDeleting(false);
        setTextIndex(0);
    }
    useEffect(() => {
        // The setTimeout-chained typing/deleting animation genuinely can't
        // be expressed as a derived render-time value — it's a timer loop
        // that advances its own state machine across renders.
        const currentText = texts[textIndex % texts.length];
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
            setTextIndex((prev) => (prev + 1) % texts.length);
        }
        return () => clearTimeout(timer);
    }, [displayedText, isDeleting, textIndex, texts, typingSpeed, deletingSpeed, pauseDuration]);
    return displayedText;
}
