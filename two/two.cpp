// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2019 Datadog, Inc.
#include "two.h"

#include "constants.h"

Two::~Two() { Py_Finalize(); }

void Two::init(const char *pythonHome) {
    if (pythonHome != NULL) {
        _pythonHome = pythonHome;
    }

    Py_SetPythonHome(const_cast<char *>(_pythonHome));
    Py_Initialize();

    PyModules::iterator it;
    for (it = _modules.begin(); it != _modules.end(); ++it) {
        Py_InitModule(builtins::getExtensionModuleName(it->first).c_str(), &_modules[it->first][0]);
    }

    // In recent versions of Python3 this is called from Py_Initialize already,
    // for Python2 it has to be explicit.
    PyEval_InitThreads();
}

bool Two::isInitialized() const { return Py_IsInitialized(); }

const char *Two::getPyVersion() const { return Py_GetVersion(); }

int Two::runSimpleString(const char *code) const { return PyRun_SimpleString(code); }

int Two::addModuleFunction(builtins::ExtensionModule module, MethType t, const char *funcName, void *func) {
    if (builtins::getExtensionModuleName(module) == builtins::module_unknown) {
        setError("Unknown ExtensionModule value");
        return -1;
    }

    int ml_flags;
    switch (t) {
    case Six::NOARGS:
        ml_flags = METH_NOARGS;
        break;
    case Six::ARGS:
        ml_flags = METH_VARARGS;
        break;
    case Six::KEYWORDS:
        ml_flags = METH_VARARGS | METH_KEYWORDS;
        break;
    default:
        setError("Unknown MethType value");
        return -1;
    }

    PyMethodDef def = { funcName, (PyCFunction)func, ml_flags, "" };

    if (_modules.find(module) == _modules.end()) {
        _modules[module] = PyMethods();
        // add the guard
        PyMethodDef guard = { NULL, NULL };
        _modules[module].push_back(guard);
    }

    // insert at beginning so we keep guard at the end
    _modules[module].insert(_modules[module].begin(), def);

    // success
    return 0;
}

Six::GILState Two::GILEnsure() {
    PyGILState_STATE state = PyGILState_Ensure();
    if (state == PyGILState_LOCKED) {
        return Six::GIL_LOCKED;
    }
    return Six::GIL_UNLOCKED;
}

void Two::GILRelease(Six::GILState state) {
    if (state == Six::GIL_LOCKED) {
        PyGILState_Release(PyGILState_LOCKED);
    } else {
        PyGILState_Release(PyGILState_UNLOCKED);
    }
}
