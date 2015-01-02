About
=====
cgrun is a command-line utility to run a program(or seize processes) with a temporary cgroup hierarchy.

Usage
=====
```sh
### Using cgrun for executing command

# Run `foobar` under some restrictions
sudo cgrun cpuset.cpus=0-2 cpuset.mems=0 cpu.shares=1 -- foobar arg1 arg2 arg3...

# Run `foobar` under some restrictions but inherit /foobar-generic as the parent hierarchy
sudo cgrun --parent /foobar-hierarchy cpu.shares=1 -- foobar arg1 arg2 arg3...

# You can specify the owner of hierarchy and uid/gid for executing the program
sudo cgrun -u kawamuray cpu.shares=1 -- foobar ...

### Using cgrun for already running process(es)

# For single process(excluding it's children)
cgrun -p $(pgrep hardwork | head -1) blkio.weight=16

# For whole process tree(including it's children)
cgrun -p $(pgrep hardwork | head -1) --tree blkio.weight=16

```

Why not libcgroup?
==================
- I want a functionality to create volatile cgroup hierarchy to run a command quickly under some restrictions from a terminal.
- libcgroup tools are flexible, but not easy and not simple.

Author
======
Yuto Kawamura(kawamuray) <kawamuray.dadada@gmail.com>

License
=======
MIT License. Please see LICENSE.
