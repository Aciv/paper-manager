// PDF.js 5.x uses these ES2024/newer DOM APIs while constructing its viewer.
// Older tablet browsers (for example Chromium WebViews before 119/116) lack
// them; without the guards the lazy PdfReader chunk throws, React unmounts the
// route, and the page is left with only the body background.

declare global {
  interface PromiseConstructor {
    withResolvers<T>(): {
      promise: Promise<T>;
      resolve: (value: T | PromiseLike<T>) => void;
      reject: (reason?: unknown) => void;
    };
  }

  interface AbortSignalConstructor {
    any(signals: AbortSignal[]): AbortSignal;
  }
}

if (typeof Promise.withResolvers !== 'function') {
  Promise.withResolvers = function withResolvers<T>() {
    let resolve!: (value: T | PromiseLike<T>) => void;
    let reject!: (reason?: unknown) => void;
    const promise = new Promise<T>((res, rej) => {
      resolve = res;
      reject = rej;
    });
    return { promise, resolve, reject };
  };
}

if (typeof AbortSignal.any !== 'function') {
  AbortSignal.any = function any(signals: AbortSignal[]): AbortSignal {
    const controller = new AbortController();
    const abort = (event: Event) => {
      for (const signal of signals) signal.removeEventListener('abort', abort);
      controller.abort((event.target as AbortSignal | null)?.reason);
    };
    for (const signal of signals) {
      if (signal.aborted) {
        controller.abort(signal.reason);
        return controller.signal;
      }
      signal.addEventListener('abort', abort, { once: true });
    }
    return controller.signal;
  };
}

export {};
