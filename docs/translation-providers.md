# 翻译 Provider 配置与模块指南

本文说明翻译 provider 如何通过**一个配置文件 + 独立模块**注册，以及如何新增脚本型（Chrome）或 API 型 provider。

## 结论

- 现在只有一份注册/控制文件：

  `web/src/translation/providers.config.ts`

- 每个 provider 是一个独立模块，放在：

  `web/src/translation/providers/<name>.ts`

- provider 模块默认导出一个 `TranslationProviderDescriptor`。
- 新增 provider 不再需要修改：
  - `TranslationProviderID` 联合类型
  - 中央 label 表
  - 中央 options 表
  - 中央 registry switch
  - 面板里的 provider note/hint 三元表达式
- 只需要：
  1. 新建 provider 模块文件
  2. 在 `providers.config.ts` 静态 import 并加一条注册项
  3. 写测试
- 前端下拉顺序、启用/禁用由 `providers.config.ts` 控制；label、提示、配置字段、自动优先级由 provider 模块描述符控制。

当前模块：

| Provider | 文件 | kind | 配置字段 |
|---|---|---|---|
| Microsoft | `providers/microsoft.ts` | `http` | 无 |
| Chrome | `providers/chrome.ts` | `script` | 无 |
| MyMemory | `providers/mymemory.ts` | `http` | 无 |

当前内置 provider 都不需要 API Key，所以配置 UI 不会出现；新增带 API Key 的 provider 时，只要描述符声明 `configFields`，面板会自动出现「配置」入口。

---

## 目录结构

```text
web/src/translation/
  types.ts                 # 类型、语言常量、descriptor 类型
  runtime.ts               # 错误、ProviderGate、文本预处理、语言检测、header 合并
  providers.config.ts      # 唯一的 provider 注册/启用/排序文件
  registry.ts              # 从 config 构建 registry / labels / options
  index.ts                 # translateText、缓存、settings、公开出口
  providers/
    microsoft.ts
    chrome.ts
    mymemory.ts
```

原来的 `web/src/translation.ts` 已移除；业务代码现在从：

```ts
import { ... } from './translation/index.ts';
```

导入。

---

## 为什么用静态 import，而不是运行时文件路径？

浏览器前端代码由 Vite 打包，Node 测试也使用 ESM 模块解析。不能把任意字符串（例如 `"providers/foo.ts"`）在运行时映射成本地模块；这些文件必须在构建时静态可达。

因此配置文件的写法是：

```ts
import microsoft from './providers/microsoft.ts';
import chrome from './providers/chrome.ts';
import mymemory from './providers/mymemory.ts';

export const TRANSLATION_PROVIDER_MODULES = [
  { file: './providers/microsoft.ts', module: microsoft, enabled: true },
  { file: './providers/chrome.ts', module: chrome, enabled: true },
  { file: './providers/mymemory.ts', module: mymemory, enabled: true },
];
```

- `file` 是给人看的源文件路径。
- `module` 是该文件默认导出的 descriptor。
- 数组顺序决定前端下拉顺序；`enabled: false` 可停用而不删文件。
- 新增 provider = 新文件 + 一条 import + 一条注册项。

如果以后想去掉 import 行，可以使用 Vite 的 `import.meta.glob('./providers/*.ts', { eager: true })` 自动发现模块；但当前 Node 测试环境没有 Vite 的 `import.meta.glob`，所以使用静态注册更稳定、可测试。

---

## 配置文件

`web/src/translation/providers.config.ts`：

```ts
import type { TranslationProviderDescriptor } from './types.ts';
import microsoft from './providers/microsoft.ts';
import chrome from './providers/chrome.ts';
import mymemory from './providers/mymemory.ts';

export type TranslationProviderModuleRegistration = {
  file: string;
  module: TranslationProviderDescriptor;
  enabled?: boolean;
};

export const TRANSLATION_PROVIDER_MODULES: readonly TranslationProviderModuleRegistration[] = [
  { file: './providers/microsoft.ts', module: microsoft, enabled: true },
  { file: './providers/chrome.ts', module: chrome, enabled: true },
  { file: './providers/mymemory.ts', module: mymemory, enabled: true },
];

export const TRANSLATION_PROVIDER_DESCRIPTORS: readonly TranslationProviderDescriptor[] =
  TRANSLATION_PROVIDER_MODULES
    .filter((entry) => entry.enabled !== false)
    .map((entry) => entry.module);
```

`registry.ts` 会校验 descriptor：

- id 非空且唯一
- kind 只能是 `http` / `script`
- label / note / hint 非空
- configFields 是数组
- autoPriority 是数字或 null
- createProvider 是函数

然后自动派生：

```ts
TRANSLATION_PROVIDER_LABELS
TRANSLATION_PROVIDER_REGISTRY
TRANSLATION_PROVIDER_OPTIONS
```

---

## Provider 模块契约

每个 provider 模块默认导出 descriptor：

```ts
import type {
  TranslationProviderDescriptor,
  TranslationProvider,
  TranslationProviderConfig,
} from '../types.ts';

const descriptor: TranslationProviderDescriptor = {
  id: 'my-provider',
  label: '我的翻译',
  kind: 'http', // 或 'script'
  note: '在线翻译',
  hint: '完整说明',
  configFields: [],
  autoPriority: 30, // null = 不参与自动模式
  createProvider(config) { ... },
};

export default descriptor;
```

`TranslationProvider` 接口仍然统一：

```ts
interface TranslationProvider {
  id: string;
  label: string;
  translate(
    text: string,
    sourceLanguage: TranslationLanguage,
    targetLanguage: TranslationLanguage,
    signal?: AbortSignal,
  ): Promise<{ text: string; detectedLanguage?: ConcreteTranslationLanguage }>;
}
```

`config` 在 `createProvider(config)` 时注入，不改变 `translate` 的调用签名。

### http 与 script

- `kind: 'http'`：使用 `fetch` 调用 API；可声明 API Key、Base URL、headers 等配置字段。
- `kind: 'script'`：使用浏览器/本地能力，例如 Chrome `Translator` API；通常没有配置字段。

Chrome 的 descriptor 示例：

```ts
const descriptor: TranslationProviderDescriptor = {
  id: 'chrome',
  label: 'Chrome 本地',
  kind: 'script',
  note: '本地模型，不上传文本',
  hint: '使用 Chrome 浏览器内置的本地翻译模型，文本不会上传。',
  configFields: [],
  autoPriority: 0,
  autoEligible: async ({ sourceLanguage, targetLanguage }) => (
    chromeTranslatorApi() !== undefined
      && await chromeTranslationAvailability(sourceLanguage, targetLanguage) === 'available'
  ),
  createProvider: () => chromeTranslationProvider,
};
```

---

## 新增 Script Provider

Script provider 和 HTTP provider 的注册方式完全一样：一个模块、一个默认导出的 descriptor、一条配置项。

### 1. 新建脚本模块

例如 `web/src/translation/providers/my-script.ts`：

```ts
import type {
  TranslationProvider,
  TranslationProviderDescriptor,
  TranslationProviderResult,
} from '../types.ts';

function getMyScriptApi() {
  return (globalThis as unknown as {
    MyTranslationApi?: {
      translate(text: string): Promise<string>;
    };
  }).MyTranslationApi;
}

const provider: TranslationProvider = {
  id: 'my-script',
  label: '我的脚本翻译',
  translate: async (text): Promise<TranslationProviderResult> => {
    const api = getMyScriptApi();
    if (!api) {
      throw new Error('当前环境没有 MyTranslationApi。');
    }
    return { text: await api.translate(text) };
  },
};

const descriptor: TranslationProviderDescriptor = {
  id: 'my-script',
  label: '我的脚本翻译',
  kind: 'script',
  note: '使用浏览器脚本能力',
  hint: '依赖当前浏览器页面提供的 MyTranslationApi。',
  configFields: [],
  autoPriority: null, // 默认仅手动选择
  createProvider: () => provider,
};

export default descriptor;
```

### 2. 在配置文件中注册

```ts
import myScript from './providers/my-script.ts';

export const TRANSLATION_PROVIDER_MODULES = [
  // ...
  { file: './providers/my-script.ts', module: myScript, enabled: true },
];
```

完成。下拉、registry、label、options 都会自动生成。

### 3. Chrome 现在如何指定模块调用

Chrome 的实际调用链：

```text
providers.config.ts
  import chrome from './providers/chrome.ts'
  { module: chrome, enabled: true }
        ↓
registry.ts
  TRANSLATION_PROVIDER_REGISTRY.chrome = descriptor
        ↓
index.ts translateText()
  descriptor.createProvider(config)
        ↓
chrome.ts
  createProvider: () => chromeTranslationProvider
        ↓
translateChrome()
        ↓
getChromeTranslator()
        ↓
globalThis.Translator.create(...)
  translator.translate(chunk)
```

关键代码：

- `web/src/translation/providers/chrome.ts`
  - `chromeTranslatorApi()` 读取 `globalThis.Translator`
  - `getChromeTranslator()` 调用浏览器内置 API
  - `translateChrome()` 调用 `translator.translate(...)`
  - descriptor 的 `createProvider: () => chromeTranslationProvider`
- `web/src/translation/providers.config.ts`
  - 静态 import Chrome 模块
  - 注册项 `{ file: './providers/chrome.ts', module: chrome, enabled: true }`
- `web/src/translation/index.ts`
  - `const provider = descriptor.createProvider(config);`
  - `await provider.translate(...)`

所以 Chrome **不是**在运行时加载某个外部脚本路径；`chrome.ts` 本身就是静态注册的 provider 模块，它调用的是浏览器内置的 `Translator` 全局对象。

### 4. 脚本 provider 的可用性/特殊 UI

通用注册表只会自动处理：

- provider 下拉
- label / note / hint
- 配置字段
- 自动模式排序/eligibility

如果脚本 provider 需要类似 Chrome 的：

- availability 检查
- 模型下载进度
- 专属状态横幅
- “下载并启用”按钮

则需要在 `TranslationPanel.tsx` 里增加对应 UI。Chrome 是特例，面板里仍有 Chrome 专用状态渲染；其他 script provider 默认只有普通 provider 行为。

### 5. 外部脚本怎么办？

如果 provider 依赖一个外部 JS 文件：

- 由 provider 模块自己负责加载（动态 import、`<script>` 注入、等待全局对象就绪等）。
- registry 不负责从配置里的字符串加载任意脚本 URL；浏览器的模块/打包限制决定了它必须静态可达，或由 provider 模块在运行时显式加载。
- 加载失败时抛 `TranslationError` 或普通 Error，让面板显示错误。

---

## 自动模式

自动模式由描述符控制，不在面板或 registry 里写死：

```ts
autoPriority: number | null
autoEligible?: (context) => boolean | Promise<boolean>
```

- `autoPriority` 越小越先尝试；`null` 表示只能手动选择。
- 缺少 `required` 配置字段的 provider 会被自动跳过。
- `autoEligible` 用于动态判断，例如 Chrome 模型是否已经可用。
- 当前顺序是：

```text
Chrome（模型已就绪，priority 0）
  -> Microsoft（10）
  -> MyMemory（20）
```

显式选择 provider 时不会 fallback；自动模式才会继续尝试下一个。

---

## 配置字段与统一请求头

### 配置字段

在 descriptor 中声明：

```ts
configFields: [
  {
    key: 'baseUrl',
    label: '服务地址',
    type: 'url',
    required: true,
    placeholder: 'https://translate.example.com',
  },
  {
    key: 'headers',
    label: '请求头 JSON',
    type: 'json',
    placeholder: '{"Authorization":"Bearer sk-..."}',
  },
  {
    key: 'apiKey',
    label: 'API Key',
    type: 'password',
    description: '也可以直接放进 headers JSON。',
  },
]
```

支持的类型：

- `text`
- `password`
- `url`
- `json`

选中该 provider 时，面板引擎行右侧会自动出现「配置」按钮，展开后生成对应表单。配置保存到：

```ts
TranslationSettings.providerConfigs[providerID]
```

并随翻译设置保存在 localStorage。

### 统一 headers

HTTP provider 使用：

```ts
providerHttpHeaders(providerID, config, defaults)
```

合并顺序：

1. provider 默认 headers
2. `config.headers` JSON
3. `config.apiKey` / `apiKeyHeader` / `apiKeyPrefix`

例如：

```ts
providerHttpHeaders('my-provider', {
  headers: '{"X-Tenant":"team-a"}',
  apiKey: 'sk-123',
  apiKeyHeader: 'Authorization',
  apiKeyPrefix: 'Bearer',
}, {
  'Content-Type': 'application/json',
});
```

结果：

```json
{
  "Content-Type": "application/json",
  "X-Tenant": "team-a",
  "Authorization": "Bearer sk-123"
}
```

所以 API Key、租户、自定义认证头都可以统一通过配置进入请求的 JSON header。

---

## 新增一个 Provider

以 `libre` 为例。

### 1. 新建模块

`web/src/translation/providers/libre.ts`：

```ts
import type {
  TranslationLanguage,
  TranslationProvider,
  TranslationProviderConfig,
  TranslationProviderDescriptor,
  TranslationProviderResult,
} from '../types.ts';
import {
  TranslationError,
  ProviderGate,
  detectSourceLanguage,
  isAbortError,
  providerHttpHeaders,
  resolveTargetLanguage,
  splitTextByCodePoints,
} from '../runtime.ts';

const gate = new ProviderGate(250);

function libreLanguage(language: TranslationLanguage): string {
  // 按目标服务实际语言代码调整。
  if (language === 'zh-Hans') return 'zh';
  return language;
}

async function translateLibre(
  text: string,
  sourceLanguage: TranslationLanguage,
  targetLanguage: TranslationLanguage,
  signal: AbortSignal | undefined,
  config: TranslationProviderConfig,
): Promise<TranslationProviderResult> {
  const normalized = text.trim();
  if (!normalized) return { text: '' };

  const source = sourceLanguage === 'auto'
    ? detectSourceLanguage(normalized)
    : sourceLanguage;
  const target = resolveTargetLanguage(normalized, targetLanguage, source);
  if (source === target) return { text: normalized, detectedLanguage: source };

  const baseUrl = (config.baseUrl ?? '').trim().replace(/\/$/, '');
  if (!baseUrl) {
    throw new TranslationError('请先配置 LibreTranslate 服务地址。', {
      provider: 'libre',
      retryable: false,
    });
  }

  const chunks = splitTextByCodePoints(normalized, 4000);
  const translations: string[] = [];

  for (const chunk of chunks) {
    if (signal?.aborted) throw new DOMException('Aborted', 'AbortError');

    let response: Response;
    try {
      response = await fetch(`${baseUrl}/translate`, {
        method: 'POST',
        headers: providerHttpHeaders('libre', config, {
          'Content-Type': 'application/json',
          Accept: 'application/json',
        }),
        body: JSON.stringify({
          q: chunk,
          source: libreLanguage(source),
          target: libreLanguage(target),
          format: 'text',
        }),
        signal,
        credentials: 'omit',
        cache: 'no-store',
      });
    } catch (error) {
      if (isAbortError(error)) throw error;
      throw new TranslationError('无法连接 LibreTranslate。', {
        provider: 'libre',
        retryable: true,
      });
    }

    if (!response.ok) {
      throw new TranslationError(`LibreTranslate 请求失败（HTTP ${response.status}）。`, {
        provider: 'libre',
        status: response.status,
        retryable: response.status === 429 || response.status >= 500,
      });
    }

    const payload = await response.json() as { translatedText?: unknown };
    if (typeof payload.translatedText !== 'string' || !payload.translatedText.trim()) {
      throw new TranslationError('LibreTranslate 返回了空译文。', {
        provider: 'libre',
        retryable: true,
      });
    }
    translations.push(payload.translatedText);
  }

  return { text: translations.join('\n'), detectedLanguage: source };
}

function createLibreProvider(config: TranslationProviderConfig): TranslationProvider {
  return {
    id: 'libre',
    label: 'LibreTranslate',
    translate: (text, sourceLanguage, targetLanguage, signal) =>
      gate.run(() => translateLibre(text, sourceLanguage, targetLanguage, signal, config)),
  };
}

const descriptor: TranslationProviderDescriptor = {
  id: 'libre',
  label: 'LibreTranslate',
  kind: 'http',
  note: '自建 LibreTranslate',
  hint: '通过配置服务地址和请求头使用自建 LibreTranslate。',
  configFields: [
    {
      key: 'baseUrl',
      label: '服务地址',
      type: 'url',
      required: true,
      placeholder: 'https://translate.example.com',
    },
    {
      key: 'headers',
      label: '请求头 JSON',
      type: 'json',
      placeholder: '{"Authorization":"Bearer ..."}',
    },
    {
      key: 'apiKey',
      label: 'API Key',
      type: 'password',
    },
  ],
  autoPriority: 30,
  autoEligible: ({ config }) => Boolean(config.baseUrl?.trim()),
  createProvider: createLibreProvider,
};

export default descriptor;
```

### 2. 在唯一配置文件中注册

`web/src/translation/providers.config.ts`：

```ts
import libre from './providers/libre.ts';

export const TRANSLATION_PROVIDER_MODULES = [
  { file: './providers/microsoft.ts', module: microsoft, enabled: true },
  { file: './providers/chrome.ts', module: chrome, enabled: true },
  { file: './providers/mymemory.ts', module: mymemory, enabled: true },
  { file: './providers/libre.ts', module: libre, enabled: true }, // 新增
];
```

完成。

不需要再改：

- `TranslationProviderID`（现在是 `string`）
- 中央 label 表
- 中央 options 表
- `TranslationPanel.tsx` 的 provider note/hint
- registry 的 switch

`registry.ts` 会自动生成 label、registry、options；面板自动出现 LibreTranslate 和「配置」按钮。

### 3. 测试

```ts
test('LibreTranslate uses configured URL and headers', async () => {
  const originalFetch = globalThis.fetch;
  const calls: Array<{ url: string; init?: RequestInit }> = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init });
    return new Response(JSON.stringify({ translatedText: '你好' }), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    });
  }) as typeof fetch;

  try {
    const provider = TRANSLATION_PROVIDER_REGISTRY.libre.createProvider({
      baseUrl: 'https://translate.example.com',
      apiKey: 'secret',
      apiKeyHeader: 'Authorization',
      apiKeyPrefix: 'Bearer',
      headers: '{"X-Tenant":"team-a"}',
    });
    const result = await provider.translate('Hello', 'auto', 'zh-Hans');
    assert.equal(result.text, '你好');
    assert.equal(calls[0].url, 'https://translate.example.com/translate');
    const headers = new Headers(calls[0].init?.headers);
    assert.equal(headers.get('authorization'), 'Bearer secret');
    assert.equal(headers.get('x-tenant'), 'team-a');
  } finally {
    globalThis.fetch = originalFetch;
  }
});
```

验证：

```bash
cd web
npm run test:translation
npm run test:selection
npm run build
```

---

## 缓存

缓存 key：

```text
providerID + serialized config + sourceLanguage + targetLanguage + text
```

修改 API Key、Base URL、headers JSON 不会复用旧配置的缓存。缓存仍是内存 LRU + TTL。

---

## CORS 与安全

- HTTP provider 是浏览器直连，必须允许 CORS 和请求头预检。
- 不支持 CORS 的服务需要 Go 代理。
- `providerConfigs` 存在 localStorage；同源脚本可读。
- 私有 API Key 建议放 Go 后端；不要提交到仓库或日志。

---

## 当前限制

- Provider 接口仍是单段文本，没有批量/流式接口。
- Provider 模块静态导入；没有运行时任意文件加载（浏览器打包限制）。
- Chrome 的模型下载/进度 UI 仍是面板专用逻辑。
- 配置字段类型只有 text/password/url/json。
- 自动模式只按 `autoPriority` 排序，没有成功率权重。
- 显式 provider 不 fallback，自动模式才继续尝试下一个。

---

## 快速检查清单

- [ ] 在 `web/src/translation/providers/` 新建模块
- [ ] 默认导出 `TranslationProviderDescriptor`
- [ ] descriptor 的 id/label/kind/note/hint 完整
- [ ] HTTP provider 使用 `providerHttpHeaders()`
- [ ] 声明 configFields（如需 API Key/URL/headers）
- [ ] 设置 `autoPriority`（或 null）
- [ ] 在 `providers.config.ts` import 并添加注册项
- [ ] 需要时设置 `enabled: false` 停用
- [ ] 添加 provider 测试和 headers 测试
- [ ] `npm run test:translation`
- [ ] `npm run test:selection`
- [ ] `npm run build`
