# -*- coding: utf-8 -*-
"""pytest 全局配置。

保证无论从哪个目录运行 pytest，``main.py`` 都能完成自举导入
（它会 `import routes`，需要自身所在目录在 sys.path 中）。
"""
import os
import sys

_SERVICE_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

if _SERVICE_DIR not in sys.path:
    sys.path.insert(0, _SERVICE_DIR)
