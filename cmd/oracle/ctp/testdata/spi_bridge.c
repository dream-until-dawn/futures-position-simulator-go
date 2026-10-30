/* 仅测试：从独立Windows线程调用已核真实SPI方法，无API、网络或凭据。 */
#include <windows.h>
#include <stdint.h>
#include <stdlib.h>
#include <stdbool.h>

struct fixture { void *method, *spi; int id, code, flags; };
static DWORD WINAPI worker(void *arg) {
    struct fixture *f = arg;
    void *row = calloc(1, 4096), *info = calloc(1, 4096);
    if (!row || !info) { free(row); free(info); return 1; }
    *(int32_t *)info = f->code;
    if (f->flags & 16) {
        typedef void (*front)(void *);
        ((front)f->method)(f->spi);
    } else if (f->flags & 32) {
        typedef void (*disconnected)(void *, int);
        ((disconnected)f->method)(f->spi, f->code);
    } else if (f->flags & 8) {
        typedef void (*generic)(void *, void *, int, bool);
        ((generic)f->method)(f->spi, f->flags & 2 ? NULL : info, f->id, (f->flags & 4) != 0);
    } else {
        typedef void (*response)(void *, void *, void *, int, bool);
        ((response)f->method)(f->spi, f->flags & 1 ? NULL : row, f->flags & 2 ? NULL : info, f->id, (f->flags & 4) != 0);
    }
    free(row); free(info);
    return 0;
}

__declspec(dllexport) uintptr_t RunFixture(void *method, void *spi, int id, int code, int flags) {
    struct fixture f = {method, spi, id, code, flags};
    DWORD thread_id, exit_code = 1;
    HANDLE thread = CreateThread(NULL, 0, worker, &f, 0, &thread_id);
    if (!thread) return 0;
    /* 必须等线程结束，不能超时后让工作线程访问已退出栈。测试进程另有总超时。 */
    if (WaitForSingleObject(thread, INFINITE) == WAIT_OBJECT_0) GetExitCodeThread(thread, &exit_code);
    CloseHandle(thread);
    return exit_code == 0 ? (uintptr_t)thread_id : 0;
}
