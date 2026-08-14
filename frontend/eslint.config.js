import js from '@eslint/js';
import globals from 'globals';
import jsxA11y from 'eslint-plugin-jsx-a11y';
import reactHooks from 'eslint-plugin-react-hooks';
import reactRefresh from 'eslint-plugin-react-refresh';
import tseslint from 'typescript-eslint';
import { defineConfig, globalIgnores } from 'eslint/config';

// Files that already violated the accessibility rules when they were turned on,
// listed so the rules can be errors everywhere else. This is a ratchet, not an
// exemption: a file leaves this list when it is fixed, and nothing may be added
// to it.
//
// It exists because the alternative was to keep the rules off until 67
// violations across 18 files had been fixed — which meant writing the playlist
// picker, the largest interactive surface this app will gain in one go, with no
// rule watching it.
//
// DownloadQueue.tsx is deliberately absent: it was fixed in the same change that
// added this list, both to prove the ratchet turns and because its four status
// filters were the worst case here — <div onClick> with no role, no tabIndex and
// no key handler, so a user without a mouse could not filter the queue at all.
const a11yBaseline = [
    'src/components/AlbumInfo.tsx',
    'src/components/ArtistInfo.tsx',
    'src/components/AudioAnalysisPage.tsx',
    'src/components/AudioConverterPage.tsx',
    'src/components/DragDropTextarea.tsx',
    'src/components/FetchHistory.tsx',
    'src/components/FileManagerPage.tsx',
    'src/components/Header.tsx',
    'src/components/HistoryPage.tsx',
    'src/components/LoginPage.tsx',
    'src/components/SearchBar.tsx',
    'src/components/SpectrumVisualization.tsx',
    'src/components/TrackList.tsx',
    'src/components/WatchlistPage.tsx',
    'src/components/settings/ApiKeysTab.tsx',
    'src/components/settings/FilesTab.tsx',
    'src/components/ui/pagination.tsx',
];

export default defineConfig([
    globalIgnores(['dist']),
    {
        files: ['**/*.{ts,tsx}'],
        extends: [
            js.configs.recommended,
            tseslint.configs.recommended,
            reactHooks.configs.flat.recommended,
            reactRefresh.configs.vite,
            // The plugin's peer range stops at eslint 9 and this project is on
            // 10, because it has not been republished since October 2024. It
            // runs correctly regardless — the range is stale, not a statement
            // about compatibility — so bun installs it and npm needs
            // --legacy-peer-deps.
            jsxA11y.flatConfigs.recommended,
        ],
        languageOptions: {
            ecmaVersion: 2020,
            globals: globals.browser,
        },
        rules: {
            // Not in the recommended set, and it is the one that catches what an
            // audit of this app actually found: controls carrying nothing but an
            // icon, which a screen reader announces as "button" and nothing else.
            // `title` counts as a label here because this codebase uses it for
            // exactly that on icon buttons.
            'jsx-a11y/control-has-associated-label': ['error', {
                labelAttributes: ['aria-label', 'title'],
                controlComponents: ['Button'],
            }],
            // The codebase already uses a leading underscore to mark
            // intentionally-unused params/vars (lib/api.ts, lib/rpc.ts,
            // vite.config.ts) — recognize that convention instead of
            // flagging it.
            '@typescript-eslint/no-unused-vars': ['error', {
                argsIgnorePattern: '^_',
                varsIgnorePattern: '^_',
                caughtErrorsIgnorePattern: '^_',
            }],
        },
    },
    {
        files: a11yBaseline,
        rules: {
            'jsx-a11y/click-events-have-key-events': 'warn',
            'jsx-a11y/no-static-element-interactions': 'warn',
            'jsx-a11y/no-noninteractive-element-interactions': 'warn',
            'jsx-a11y/control-has-associated-label': 'warn',
            'jsx-a11y/label-has-associated-control': 'warn',
            'jsx-a11y/anchor-has-content': 'warn',
            'jsx-a11y/no-autofocus': 'warn',
        },
    },
]);
