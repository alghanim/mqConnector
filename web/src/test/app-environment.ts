// Stub for `$app/environment` so stores that import `browser` work under
// jsdom. We are always "in the browser" during unit tests.
export const browser = true;
export const dev = true;
export const building = false;
export const version = 'test';
