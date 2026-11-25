/*
 * Preprocessor directives: '#' first on its line, to the end of the
 * logical line.
 */
#ifndef SAMPLE_H
# define SAMPLE_H

#include <std.h>
# include "local.h"	/* a comment after it */
#include "/include/config.h" // and a line comment
#include SAMPLE_PATH

#define LIMIT 10
#define EMPTY
#define MAX(a, b) ((a) > (b) ? (a) : (b))
#define STR(x) #x
#define PASTE(a, b) a ## b
#define DIRS ({ "north", "south" })
#define EXITS ([ "north": "/room/hall", \
                 "south": DIRS[1] ])
#define LONG_TEXT "one line\n\
another line\n"
#define OPEN ([
#define CLOSE ])
#define MULTI(x) do { \
        x; /* a comment that
              goes on */ \
    } while (0)

#if defined(__DGD__) && LIMIT > 0x10 || !defined(OTHER)
# undef LIMIT
#elif LIMIT == 3
# line 100 "sample.h"
# pragma unconstrained_float
#else
# error this can't be right: "unbalanced
#endif

#ifdef OTHER
int after_a_directive;
#endif
  #define INDENTED 1
#unknown directive
#

#endif /* SAMPLE_H */
