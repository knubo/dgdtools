/*
 * An object the way the formatter leaves one: this file is a fixed
 * point of `dgdtools format`, and what the Vim indent script must
 * reproduce from the same lines with their indentation taken away.
 */
#include <std.h>

inherit room "/std/room";

#define DOOR_KEY "brass key"

private static mapping exits;
private int *counts;

static void
create()
{
    room::create();
    exits = ([
        "north": "/room/hall",
        "south": "/room/yard",
    ]);
    counts = ({
        1,
        2,
        3,
    });
    set_short("a sample room");
    set_long("A room with two exits, " +
        "described over two lines.\n");
}

/*
 * A comment of several lines
 * before a function.
 */
int
query_count(int which, varargs int fallback)
{
    int i, total;

    if (which < 0 || which >= sizeof(counts)) {
        return fallback;
    }
    for (i = 0; i < which; i++) {
        total += counts[i];
    }
    if (total > 10)
        total = 10;
    else
        total++;
    while (total > 5) {
        total--;
    }
    do {
        total++;
    } while (total < 3);
    switch (which) {
    case 0:
        total = 0;
        break;
    case 1..2:
        total = call_other(this_object(), "query_bonus",
            which, total);
        break;
    default:
        break;
    }
#ifdef DEBUG
    write("total: " + total + "\n");
#endif
    return total;
}

string
query_exit(string dir)
{
    string dest;

    dest = exits[dir];
    if (!dest) {
        // nothing that way
        return nil;
    }
    return catch (dest->load()) ? nil : dest;
}
